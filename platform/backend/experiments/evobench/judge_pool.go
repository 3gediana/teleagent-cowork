package main

// Concurrent judge execution pool.
//
// Why it exists: reasoning-model judge calls (MiniMax-M2.7, DeepSeek-R1)
// take 20-40 seconds each because most tokens go into <think>. Running
// 25 rounds sequentially is ~12 minutes of walltime even though each
// call is independent. A bounded worker pool cuts that to ~30-60 s.
//
// The pool is deliberately simple — no retry, no cancellation. Errors
// flow back as Skipped results, same semantics as synchronous
// JudgeRank. Callers own the aggregation.

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/a3c/platform/internal/service"
)

// JudgeTask is one queued judge request captured during the
// simulation phase. We stash the full top-K slice so later
// processing can still do training / eval without re-running the
// selector.
type JudgeTask struct {
	RoundIndex int
	QueryDesc  string
	Ranked     []service.InjectedArtifact
	Candidates []JudgeCandidate
	// ForTraining flags whether this round's pairs go into the
	// training dataset; eval-only rounds set it false.
	ForTraining bool
}

// JudgeTaskResult is one task's resolved verdict, pairing the
// original task with the parsed model output.
type JudgeTaskResult struct {
	Task   JudgeTask
	Result JudgeResult
}

// RunJudgePool executes all tasks concurrently with at most
// `workers` in flight. Preserves task order in the returned slice
// so downstream code can deterministically assign training vs eval
// and reproduce results with a fixed seed.
//
// Emits a one-line progress dot for every completed task so long
// runs don't appear hung.
func RunJudgePool(ctx context.Context, cfg JudgeConfig, tasks []JudgeTask, workers int) []JudgeTaskResult {
	if len(tasks) == 0 {
		return nil
	}
	if workers < 1 {
		workers = 1
	}
	if workers > len(tasks) {
		workers = len(tasks)
	}

	results := make([]JudgeTaskResult, len(tasks))
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup

	var done int64
	total := int64(len(tasks))
	fmt.Printf("Judge pool: %d tasks × %d workers\n", len(tasks), workers)
	start := time.Now()

	for i := range tasks {
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int) {
			defer wg.Done()
			defer func() { <-sem }()
			jr := JudgeRank(ctx, cfg, tasks[idx].QueryDesc, tasks[idx].Candidates)
			results[idx] = JudgeTaskResult{Task: tasks[idx], Result: jr}
			n := atomic.AddInt64(&done, 1)
			if n%2 == 0 || n == total {
				fmt.Printf("  judge %d/%d (elapsed %.0fs)\n",
					n, total, time.Since(start).Seconds())
			}
		}(i)
	}
	wg.Wait()
	fmt.Printf("Judge pool done in %.0fs\n", time.Since(start).Seconds())
	return results
}
