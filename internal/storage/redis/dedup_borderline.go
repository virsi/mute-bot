package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/redis/go-redis/v9"
)

// BorderlinePair carries the minimum needed by the reconciler to call
// LLMJudge: the incoming post id, a candidate post id, and the cosine
// distance that put them into the borderline band.
type BorderlinePair struct {
	PostID      int64   `json:"post_id"`
	CandidateID int64   `json:"candidate_id"`
	Distance    float32 `json:"distance"`
}

// BorderlineQueue is the Redis list dedup:borderline drained by the
// reconciler goroutine. Push trims the list to max on every call so a
// stuck reconciler cannot make Redis OOM.
type BorderlineQueue struct {
	rdb *redis.Client
	key string
	max int
}

// NewBorderlineQueue returns a queue bound to the dedup:borderline list.
// maxLen caps the list size on every push; 5000 is a sane default for a
// 5-minute reconciler cadence.
func NewBorderlineQueue(c *Client, maxLen int) *BorderlineQueue {
	if maxLen <= 0 {
		maxLen = 5000
	}
	return &BorderlineQueue{rdb: c.RDB(), key: "dedup:borderline", max: maxLen}
}

// Push appends a pair to the right of the list and trims the head if the
// list exceeds max. The MULTI keeps push+trim atomic.
func (q *BorderlineQueue) Push(ctx context.Context, p BorderlinePair) error {
	b, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	pipe := q.rdb.TxPipeline()
	pipe.RPush(ctx, q.key, b)
	pipe.LTrim(ctx, q.key, int64(-q.max), -1)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("push: %w", err)
	}
	return nil
}

// Drain pops up to limit pairs from the left of the list and returns them.
// Corrupted entries are skipped so a single bad payload does not stall the
// reconciler.
func (q *BorderlineQueue) Drain(ctx context.Context, limit int) ([]BorderlinePair, error) {
	out := make([]BorderlinePair, 0, limit)
	for i := 0; i < limit; i++ {
		b, err := q.rdb.LPop(ctx, q.key).Bytes()
		if errors.Is(err, redis.Nil) {
			break
		}
		if err != nil {
			return out, fmt.Errorf("lpop: %w", err)
		}
		var p BorderlinePair
		if err := json.Unmarshal(b, &p); err != nil {
			continue
		}
		out = append(out, p)
	}
	return out, nil
}
