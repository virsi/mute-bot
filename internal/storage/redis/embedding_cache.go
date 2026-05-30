package redis

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// EmbeddingCache stores text-hash → embedding vector. Entries expire after
// ttlSecs — the dedup pipeline only needs the recent window.
type EmbeddingCache struct {
	c       *Client
	ttlSecs int
}

// NewEmbeddingCache constructs an embedding cache. A zero ttlSecs defaults
// to one week — long enough to absorb cyclical reposts but short enough to
// bound storage.
func NewEmbeddingCache(c *Client, ttlSecs int) *EmbeddingCache {
	if ttlSecs == 0 {
		ttlSecs = 7 * 24 * 3600
	}
	return &EmbeddingCache{c: c, ttlSecs: ttlSecs}
}

// Get returns the cached vector for hash. The ok return is false when the
// key is absent — distinguished from real errors via errors.Is on redis.Nil.
func (e *EmbeddingCache) Get(ctx context.Context, hash [32]byte) ([]float32, bool, error) {
	key := embKey(hash)
	raw, err := e.c.RDB().Get(ctx, key).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("get: %w", err)
	}
	var v []float32
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, false, fmt.Errorf("decode: %w", err)
	}
	return v, true, nil
}

// Set writes v for hash with the configured TTL. JSON encoding keeps the
// vector portable across Redis CLI/tools; binary encoding would be tighter
// but adds a custom protocol with no clear win at the cache's lifetime.
func (e *EmbeddingCache) Set(ctx context.Context, hash [32]byte, v []float32) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("encode: %w", err)
	}
	key := embKey(hash)
	if err := e.c.RDB().Set(ctx, key, raw, time.Duration(e.ttlSecs)*time.Second).Err(); err != nil {
		return fmt.Errorf("set: %w", err)
	}
	return nil
}

// embKey derives the Redis key for a given text hash.
func embKey(hash [32]byte) string {
	return "emb:" + hex.EncodeToString(hash[:])
}
