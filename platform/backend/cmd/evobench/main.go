package main

// evobench — synthetic self-evolution benchmark
// =============================================
//
// Simulates the full retrieval → injection → audit → feedback loop
// against an in-memory SQLite DB with hand-crafted embeddings.
// No LLM, no Redis, no MySQL. Outputs statistics that answer:
//
//   1. RRF ranking quality: does semantic actually win?
//   2. Session diversity: does the cap prevent cluster monopolisation?
//   3. Attribution accuracy: does rank-based feedback credit the right artifacts?
//   4. Feedback convergence: do success/failure counts diverge over rounds?
//
// Usage:
//   go run ./cmd/evobench
//   go run ./cmd/evobench -rounds 50 -artifacts 80 -seed 42

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"math/rand"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/a3c/platform/internal/model"
	"github.com/a3c/platform/internal/service"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Suppress noisy feedback logs during benchmark.
func init() {
	log.SetOutput(os.Stderr)
	orig := log.Writer()
	_ = orig
	log.SetOutput(new(devNullWriter))
}

type devNullWriter struct{}
func (devNullWriter) Write(p []byte) (int, error) { return len(p), nil }

// -- flags ----------------------------------------------------------------

var (
	flagRounds    = envInt("EVOBENCH_ROUNDS", 30)
	flagArtifacts = envInt("EVOBENCH_ARTIFACTS", 60)
	flagSeed      = envInt("EVOBENCH_SEED", 0) // 0 = use current time
)

func envInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n := def
	fmt.Sscanf(v, "%d", &n)
	return n
}

// -- main -----------------------------------------------------------------

func main() {
	seed := int64(flagSeed)
	if seed == 0 {
		seed = time.Now().UnixNano()
	}
	rng := rand.New(rand.NewSource(seed))

	fmt.Printf("╔══════════════════════════════════════════════╗\n")
	fmt.Printf("║  evobench — self-evolution synthetic bench    ║\n")
	fmt.Printf("╚══════════════════════════════════════════════╝\n")
	fmt.Printf("rounds=%d  artifacts=%d  seed=%d\n\n", flagRounds, flagArtifacts, seed)

	initDB()
	defer resetDB()

	// Phase 1: seed artifact pool
	artifacts := seedPool(rng, flagArtifacts)
	fmt.Printf("Seeded %d artifacts across %d clusters\n\n", len(artifacts), countClusters(artifacts))

	// Phase 2: run simulation rounds
	stats := runSimulation(rng, flagRounds)

	// Phase 3: print report
	printReport(stats)
}

// -- DB setup -------------------------------------------------------------

var prevDB *gorm.DB

func initDB() *gorm.DB {
	dsn := "file:evobench?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		log.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.KnowledgeArtifact{}, &model.Change{}); err != nil {
		log.Fatalf("migrate: %v", err)
	}
	prevDB = model.DB
	model.DB = db
	return db
}

func resetDB() {
	model.DB = prevDB
}

// -- Artifact seeding -----------------------------------------------------

type artifactMeta struct {
	ID           string
	Cluster      string // first source_events entry
	Kind         string
	TrueTopic    int // which "topic" this artifact belongs to (0..4)
	SuccessCount int
	FailureCount int
}

func seedPool(rng *rand.Rand, n int) []artifactMeta {
	kinds := []string{"pattern", "anti_pattern", "tool_recipe"}
	clusters := []string{"ep_A", "ep_B", "ep_C", "ep_D", "ep_E", "ep_F", "ep_G", "ep_H"}
	topics := 5 // 5 orthogonal semantic directions
	tagLabels := []string{"bugfix", "refactor", "feature", "test", "docs"}

	metas := make([]artifactMeta, n)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("ka_%03d", i)
		kind := kinds[rng.Intn(len(kinds))]
		cluster := clusters[rng.Intn(len(clusters))]
		topic := rng.Intn(topics)
		// Pre-existing success/failure to test importance signal
		succ := rng.Intn(6)
		fail := rng.Intn(4)

		vec := topicVec(topic, rng)
		sourceEvents := fmt.Sprintf(`["%s","%s_evt"]`, cluster, id)

		// 20% of artifacts get no embedding (simulates sidecar down)
		noEmbed := rng.Float64() < 0.20
		var embedding []byte
		var embDim int
		if !noEmbed {
			embedding = service.MarshalEmbedding(vec)
			embDim = len(vec)
		}

		// Embed tag label in payload so tagScore can pick it up
		tag := tagLabels[rng.Intn(len(tagLabels))]
		payload := fmt.Sprintf(`{"task_tag":"%s","topic":%d}`, tag, topic)

		ka := &model.KnowledgeArtifact{
			ID:           id,
			ProjectID:    "p1",
			Kind:         kind,
			Name:         fmt.Sprintf("%s_topic%d_%s", kind, topic, id),
			Summary:      fmt.Sprintf("Summary for %s about topic %d tag %s", kind, topic, tag),
			Payload:      payload,
			Status:       "active",
			Confidence:   0.5 + rng.Float64()*0.5,
			Version:      1,
			Embedding:    embedding,
			EmbeddingDim: embDim,
			SourceEvents: sourceEvents,
			SuccessCount: succ,
			FailureCount: fail,
			UsageCount:   succ + fail,
			CreatedAt:    time.Now().Add(-time.Duration(rng.Intn(45)) * 24 * time.Hour),
			UpdatedAt:    time.Now().Add(-time.Duration(rng.Intn(10)) * 24 * time.Hour),
		}
		if err := model.DB.Create(ka).Error; err != nil {
			log.Fatalf("seed artifact %s: %v", id, err)
		}

		metas[i] = artifactMeta{
			ID: id, Cluster: cluster, Kind: kind,
			TrueTopic: topic, SuccessCount: succ, FailureCount: fail,
		}
	}
	return metas
}

// topicVec returns a unit vector in 3D space. Each topic gets a
// distinct direction; small noise makes same-topic artifacts not
// perfectly identical (realistic).
func topicVec(topic int, rng *rand.Rand) []float32 {
	// 5 base directions on the unit sphere
	bases := [][3]float32{
		{1, 0, 0},
		{0, 1, 0},
		{0, 0, 1},
		{0.707, 0.707, 0},
		{0, 0.707, 0.707},
	}
	b := bases[topic%len(bases)]
	// Add small noise
	noise := float32(0.05)
	x := b[0] + (rng.Float32()-0.5)*noise
	y := b[1] + (rng.Float32()-0.5)*noise
	z := b[2] + (rng.Float32()-0.5)*noise
	// Normalise
	norm := float32(math.Sqrt(float64(x*x + y*y + z*z)))
	if norm == 0 {
		norm = 1
	}
	return []float32{x / norm, y / norm, z / norm}
}

func countClusters(metas []artifactMeta) int {
	seen := map[string]struct{}{}
	for _, m := range metas {
		seen[m.Cluster] = struct{}{}
	}
	return len(seen)
}

// -- Simulation -----------------------------------------------------------

type simStats struct {
	Rounds int

	// Retrieval stats
	TotalInjected       int
	AvgInjectedPerRound float64
	SemanticWins        int // top-1 artifact's dominant signal is semantic
	TagWins             int
	ImportanceWins      int
	RecencyWins         int
	FallbackWins        int

	// Degraded-mode stats (no query embedding)
	DegradedRounds      int
	DegradedSemanticWins int
	DegradedTagWins     int
	DegradedImportanceWins int
	DegradedRecencyWins int
	DegradedFallbackWins int

	// Diversity stats
	ClusterCounts      map[string]int // cluster → how many times it appeared in top-1
	SameClusterTop3    int            // rounds where top-3 all share a cluster
	DiversityCapFired  int            // rounds where diversity cap removed ≥1 artifact

	// Attribution stats (from HandleChangeAudit)
	L0Count            int
	L1Count            int
	L2Count            int
	AttributedAvg      float64 // average # of artifacts credited per round
	TopScoredCredited  int     // rounds where the highest-scored artifact was credited
	TopScoredTotal     int     // rounds with score data available

	// Feedback convergence
	FinalSuccessCounts []int
	FinalFailureCounts []int
	ScoreDivergence    float64 // std dev of (success - failure) across artifacts
}

func runSimulation(rng *rand.Rand, rounds int) simStats {
	stats := simStats{
		ClusterCounts: map[string]int{},
	}

	for r := 0; r < rounds; r++ {
		// Pick a random query topic
		queryTopic := rng.Intn(5)
		queryVec := topicVec(queryTopic, rng)

		// 30% of rounds: simulate no embedding available (sidecar down)
		// This tests RRF graceful degradation — semantic should wash out
		noEmbed := rng.Float64() < 0.30
		if noEmbed {
			queryVec = nil
			stats.DegradedRounds++
		}

		// Pick random tag to simulate task-tag signal
		tagLabels := []string{"bugfix", "refactor", "feature", "test", "docs"}
		tag := tagLabels[rng.Intn(len(tagLabels))]
		weightedTags := []service.WeightedTag{
			{Tag: tag, Weight: 0.6 + rng.Float64()*0.4}, // 0.6-1.0
		}

		// Retrieve artifacts
		result := service.SelectArtifactsForInjection(context.Background(), service.ArtifactQuery{
			ProjectID:      "p1",
			Audience:       service.AudienceCoder,
			QueryEmbedding: queryVec,
			WeightedTags:   weightedTags,
		})

		stats.TotalInjected += len(result)
		stats.Rounds++

		if len(result) == 0 {
			continue
		}

		// Track dominant signal of top-1
		top1 := result[0]
		sig := dominantSignal(top1.Reason)
		if noEmbed {
			switch sig {
			case "semantic":
				stats.DegradedSemanticWins++
			case "tag":
				stats.DegradedTagWins++
			case "importance":
				stats.DegradedImportanceWins++
			case "recency":
				stats.DegradedRecencyWins++
			default:
				stats.DegradedFallbackWins++
			}
		} else {
			switch sig {
			case "semantic":
				stats.SemanticWins++
			case "tag":
				stats.TagWins++
			case "importance":
				stats.ImportanceWins++
			case "recency":
				stats.RecencyWins++
			default:
				stats.FallbackWins++
			}
		}

		// Track cluster diversity in top-3
		topN := 3
		if len(result) < topN {
			topN = len(result)
		}
		clustersInTop3 := map[string]int{}
		for i := 0; i < topN; i++ {
			clustersInTop3[clusterOf(result[i])]++
		}
		for c := range clustersInTop3 {
			stats.ClusterCounts[c]++
		}
		// Check if top-3 all share one cluster
		for _, cnt := range clustersInTop3 {
			if cnt == topN {
				stats.SameClusterTop3++
				break
			}
		}

		// Simulate audit verdict
		auditLevel := randomAuditLevel(rng)
		switch auditLevel {
		case "L0":
			stats.L0Count++
		case "L1":
			stats.L1Count++
		case "L2":
			stats.L2Count++
		}

		// Build injected refs JSON for the Change row
		refs := make([]service.InjectedRef, len(result))
		for i, ia := range result {
			refs[i] = service.InjectedRef{
				ID:     ia.Artifact.ID,
				Reason: ia.Reason,
				Score:  ia.Score,
			}
		}
		injectedJSON, _ := json.Marshal(refs)

		changeID := fmt.Sprintf("chg_%03d", r)
		if err := model.DB.Create(&model.Change{
			ID: changeID, ProjectID: "p1", AgentID: "agent_1", Version: "v1",
			InjectedArtifacts: string(injectedJSON),
		}).Error; err != nil {
			log.Fatalf("create change: %v", err)
		}

		// Run feedback attribution
		service.HandleChangeAudit(changeID, auditLevel)

		// Check if top-scored artifact was credited
		if len(refs) > 0 && refs[0].Score > 0 {
			stats.TopScoredTotal++
			// Read back the top artifact to see if it was credited
			var ka model.KnowledgeArtifact
			model.DB.Where("id = ?", refs[0].ID).First(&ka)
			if auditLevel == "L0" || auditLevel == "L1" {
				if ka.SuccessCount > 0 {
					stats.TopScoredCredited++
				}
			} else if auditLevel == "L2" {
				if ka.FailureCount > 0 {
					stats.TopScoredCredited++
				}
			}
		}
	}

	// Compute averages
	if stats.Rounds > 0 {
		stats.AvgInjectedPerRound = float64(stats.TotalInjected) / float64(stats.Rounds)
		stats.AttributedAvg = float64(stats.L0Count*2+stats.L1Count*1+stats.L2Count*2) / float64(stats.Rounds)
	}

	// Final artifact state
	var allArts []model.KnowledgeArtifact
	model.DB.Where("project_id = ?", "p1").Find(&allArts)
	stats.FinalSuccessCounts = make([]int, len(allArts))
	stats.FinalFailureCounts = make([]int, len(allArts))
	for i, a := range allArts {
		stats.FinalSuccessCounts[i] = a.SuccessCount
		stats.FinalFailureCounts[i] = a.FailureCount
	}
	stats.ScoreDivergence = stdDev(diff(stats.FinalSuccessCounts, stats.FinalFailureCounts))

	return stats
}

func randomAuditLevel(rng *rand.Rand) string {
	// Roughly: 50% L0, 30% L1, 20% L2 (matches typical audit distribution)
	r := rng.Float64()
	switch {
	case r < 0.50:
		return "L0"
	case r < 0.80:
		return "L1"
	default:
		return "L2"
	}
}

func clusterOf(ia service.InjectedArtifact) string {
	return clusterKey(ia.Artifact)
}

// clusterKey mirrors service.clusterKey but works on the model struct directly.
func clusterKey(a model.KnowledgeArtifact) string {
	if a.SourceEvents == "" {
		return ""
	}
	var ids []string
	if err := json.Unmarshal([]byte(a.SourceEvents), &ids); err != nil || len(ids) == 0 {
		return ""
	}
	return ids[0]
}

func dominantSignal(reason string) string {
	if reason == "" || reason == "fallback" {
		return "fallback"
	}
	end := len(reason)
	for i := 0; i < len(reason); i++ {
		if reason[i] == ';' {
			end = i
			break
		}
	}
	head := reason[:end]
	for i := 0; i < len(head); i++ {
		if head[i] == '=' {
			return head[:i]
		}
	}
	return head
}

// -- Report ---------------------------------------------------------------

func printReport(s simStats) {
	fmt.Println("┌─────────────────────────────────────────────┐")
	fmt.Println("│  RETRIEVAL QUALITY                          │")
	fmt.Println("├─────────────────────────────────────────────┤")
	fmt.Printf("│  Avg injected/round:    %.1f / 10 budget     │\n", s.AvgInjectedPerRound)

	total := s.SemanticWins + s.TagWins + s.ImportanceWins + s.RecencyWins + s.FallbackWins
	if total > 0 {
		fmt.Printf("│  Top-1 dominant signal:                     │\n")
		fmt.Printf("│    semantic   %3d (%4.1f%%)                  │\n", s.SemanticWins, pct(s.SemanticWins, total))
		fmt.Printf("│    tag        %3d (%4.1f%%)                  │\n", s.TagWins, pct(s.TagWins, total))
		fmt.Printf("│    importance %3d (%4.1f%%)                  │\n", s.ImportanceWins, pct(s.ImportanceWins, total))
		fmt.Printf("│    recency    %3d (%4.1f%%)                  │\n", s.RecencyWins, pct(s.RecencyWins, total))
		if s.FallbackWins > 0 {
			fmt.Printf("│    fallback   %3d (%4.1f%%)                  │\n", s.FallbackWins, pct(s.FallbackWins, total))
		}
	}

	// Degraded-mode signal distribution
	if s.DegradedRounds > 0 {
		fmt.Println("├─────────────────────────────────────────────┤")
		fmt.Printf("│  DEGRADED MODE (no query embedding): %d rnds │\n", s.DegradedRounds)
		fmt.Println("├─────────────────────────────────────────────┤")
		dt := s.DegradedSemanticWins + s.DegradedTagWins + s.DegradedImportanceWins + s.DegradedRecencyWins + s.DegradedFallbackWins
		if dt > 0 {
			fmt.Printf("│    semantic   %3d (%4.1f%%)                  │\n", s.DegradedSemanticWins, pct(s.DegradedSemanticWins, dt))
			fmt.Printf("│    tag        %3d (%4.1f%%)                  │\n", s.DegradedTagWins, pct(s.DegradedTagWins, dt))
			fmt.Printf("│    importance %3d (%4.1f%%)                  │\n", s.DegradedImportanceWins, pct(s.DegradedImportanceWins, dt))
			fmt.Printf("│    recency    %3d (%4.1f%%)                  │\n", s.DegradedRecencyWins, pct(s.DegradedRecencyWins, dt))
			if s.DegradedFallbackWins > 0 {
				fmt.Printf("│    fallback   %3d (%4.1f%%)                  │\n", s.DegradedFallbackWins, pct(s.DegradedFallbackWins, dt))
			}
		}
	}

	fmt.Println("├─────────────────────────────────────────────┤")
	fmt.Println("│  SESSION DIVERSITY                          │")
	fmt.Println("├─────────────────────────────────────────────┤")
	fmt.Printf("│  Top-3 same-cluster rounds: %d / %d (%4.1f%%) │\n",
		s.SameClusterTop3, s.Rounds, pct(s.SameClusterTop3, s.Rounds))
	fmt.Printf("│  Cluster distribution in top-1:             │\n")
	// Sort clusters by count
	type kv struct{ k string; v int }
	var cks []kv
	for k, v := range s.ClusterCounts {
		cks = append(cks, kv{k, v})
	}
	sort.Slice(cks, func(i, j int) bool { return cks[i].v > cks[j].v })
	for _, ck := range cks {
		fmt.Printf("│    %-8s %3d                               │\n", ck.k, ck.v)
	}

	fmt.Println("├─────────────────────────────────────────────┤")
	fmt.Println("│  FEEDBACK ATTRIBUTION                       │")
	fmt.Println("├─────────────────────────────────────────────┤")
	fmt.Printf("│  Audit distribution:                        │\n")
	fmt.Printf("│    L0 (success)  %3d (%4.1f%%)               │\n", s.L0Count, pct(s.L0Count, s.Rounds))
	fmt.Printf("│    L1 (partial)  %3d (%4.1f%%)               │\n", s.L1Count, pct(s.L1Count, s.Rounds))
	fmt.Printf("│    L2 (failure)  %3d (%4.1f%%)               │\n", s.L2Count, pct(s.L2Count, s.Rounds))
	fmt.Printf("│  Avg artifacts credited/round: %.1f          │\n", s.AttributedAvg)
	if s.TopScoredTotal > 0 {
		fmt.Printf("│  Top-scored artifact credited: %d / %d (%4.1f%%) │\n",
			s.TopScoredCredited, s.TopScoredTotal, pct(s.TopScoredCredited, s.TopScoredTotal))
	}

	fmt.Println("├─────────────────────────────────────────────┤")
	fmt.Println("│  FEEDBACK CONVERGENCE                       │")
	fmt.Println("├─────────────────────────────────────────────┤")
	fmt.Printf("│  Score divergence (σ of success-failure): %.2f │\n", s.ScoreDivergence)

	// Histogram of net scores
	nets := diff(s.FinalSuccessCounts, s.FinalFailureCounts)
	bins := binCounts(nets, -3, 5)
	fmt.Printf("│  Net score histogram (success - failure):    │\n")
	for b := -3; b <= 4; b++ {
		bar := strings.Repeat("█", bins[b])
		fmt.Printf("│  %+2d: %-30s  %2d │\n", b, bar, bins[b])
	}

	fmt.Println("└─────────────────────────────────────────────┘")

	// Verdict
	fmt.Println()
	if s.SemanticWins > total/3 {
		fmt.Println("✓ Semantic signal dominates retrieval — RRF is working correctly")
	} else {
		fmt.Println("⚠ Semantic signal is weak — check embedding quality or rrfK tuning")
	}
	if s.SameClusterTop3 <= s.Rounds/5 {
		fmt.Println("✓ Session diversity cap is effective — top-3 rarely same-cluster")
	} else {
		fmt.Println("⚠ Session diversity cap may be too loose — same-cluster dominance detected")
	}
	if s.TopScoredTotal > 0 && pct(s.TopScoredCredited, s.TopScoredTotal) > 90 {
		fmt.Println("✓ Rank-based attribution credits the top-scored artifact reliably")
	} else if s.TopScoredTotal > 0 {
		fmt.Println("⚠ Attribution may not be reaching the top-scored artifact consistently")
	}
	if s.ScoreDivergence > 1.0 {
		fmt.Println("✓ Feedback counts are diverging — the loop is learning")
	} else {
		fmt.Println("ℹ Feedback counts are still flat — more rounds needed for convergence")
	}
	if s.DegradedRounds > 0 {
		dt := s.DegradedSemanticWins + s.DegradedTagWins + s.DegradedImportanceWins + s.DegradedRecencyWins + s.DegradedFallbackWins
		if dt > 0 && s.DegradedSemanticWins == 0 {
			fmt.Println("✓ RRF graceful degradation works — semantic washes out when no query embedding")
		} else if dt > 0 && s.DegradedSemanticWins > 0 {
			fmt.Println("⚠ Semantic still wins in degraded mode — artifact embeddings alone may be biasing rank")
		}
	}
}

// -- helpers --------------------------------------------------------------

func pct(part, total int) float64 {
	if total == 0 {
		return 0
	}
	return 100.0 * float64(part) / float64(total)
}

func diff(a, b []int) []int {
	out := make([]int, len(a))
	for i := range a {
		out[i] = a[i] - b[i]
	}
	return out
}

func stdDev(vals []int) float64 {
	if len(vals) == 0 {
		return 0
	}
	var sum float64
	for _, v := range vals {
		sum += float64(v)
	}
	mean := sum / float64(len(vals))
	var sq float64
	for _, v := range vals {
		d := float64(v) - mean
		sq += d * d
	}
	return math.Sqrt(sq / float64(len(vals)))
}

func binCounts(vals []int, lo, hi int) map[int]int {
	bins := map[int]int{}
	for _, v := range vals {
		b := v
		if b < lo {
			b = lo
		}
		if b > hi {
			b = hi
		}
		bins[b]++
	}
	return bins
}
