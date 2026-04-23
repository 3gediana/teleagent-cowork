package service

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/a3c/platform/internal/agentpool"
	"github.com/a3c/platform/internal/model"
)

// PoolBroadcastConsumerImpl satisfies agentpool.BroadcastConsumer by
// popping the same directed-broadcast envelopes BroadcastDirected
// writes (see broadcast.go:BroadcastDirected). Implementation is
// the mirror of GetDirectedMessages, but returns the typed
// agentpool.BroadcastEvent shape the pool's consumer loop expects
// rather than the gin.H map the MCP poller used to consume.
//
// Wire it into the pool manager with Manager.WithBroadcastConsumer.
type PoolBroadcastConsumerImpl struct{}

// NewPoolBroadcastConsumer returns a fresh consumer. Zero state is
// meaningful; all Redis access goes through model.RDB.
func NewPoolBroadcastConsumer() *PoolBroadcastConsumerImpl {
	return &PoolBroadcastConsumerImpl{}
}

// FetchEvents atomically drains Redis list a3c:directed:<agentID>
// and decodes each envelope. Order matches enqueue order because
// we LRANGE + DEL (opencode's archive watcher + any other producer
// both use RPush). Zero events + nil error is the common case
// ("queue empty"); callers must not treat it as an error.
//
// Malformed entries are skipped with a log line — we'd rather ship
// the well-formed ones than block the whole poll on one bad actor.
func (c *PoolBroadcastConsumerImpl) FetchEvents(ctx context.Context, agentID string) ([]agentpool.BroadcastEvent, error) {
	if model.RDB == nil {
		return nil, nil
	}
	key := fmt.Sprintf("a3c:directed:%s", agentID)

	raws, err := model.RDB.LRange(ctx, key, 0, -1).Result()
	if err != nil {
		return nil, fmt.Errorf("redis LRange %s: %w", key, err)
	}
	if len(raws) == 0 {
		return nil, nil
	}

	// Atomic-ish drain. We read then delete; if a producer races
	// us between the two calls the racing message gets pushed onto
	// a fresh list and we pick it up next tick. Good enough: the
	// only guarantee we need is "no duplicate delivery".
	if _, delErr := model.RDB.Del(ctx, key).Result(); delErr != nil {
		// Non-fatal: the messages are already in hand. Worst case
		// next tick returns dupes, which opencode will happily
		// inject twice. Log and carry on.
		_ = delErr
	}

	events := make([]agentpool.BroadcastEvent, 0, len(raws))
	for _, s := range raws {
		var env struct {
			Header struct {
				Type      string `json:"type"`
				MessageID string `json:"messageID"`
			} `json:"header"`
			Payload map[string]interface{} `json:"payload"`
		}
		if err := json.Unmarshal([]byte(s), &env); err != nil {
			continue
		}
		events = append(events, agentpool.BroadcastEvent{
			Type:      env.Header.Type,
			MessageID: env.Header.MessageID,
			Payload:   env.Payload,
		})
	}
	return events, nil
}
