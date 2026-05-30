package redis

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

// MinHashIndexConfig parameterises the LSH bucket layout.
//
// The signature is partitioned into Bands × RowsPerBand uint32s. Two posts
// land in the same bucket of at least one band iff they share those exact
// rows — the probability of a collision rises smoothly with the underlying
// Jaccard similarity.
type MinHashIndexConfig struct {
	Bands       int
	RowsPerBand int
	TTLSecs     int
}

// MinHashIndex stores LSH bucket → post-id sets in Redis. Buckets are
// short-lived (TTLSecs) — only the active dedup window needs to be queried.
type MinHashIndex struct {
	c   *Client
	cfg MinHashIndexConfig
}

// NewMinHashIndex constructs an index. Zero-values in cfg are replaced with
// defaults (16 bands × 8 rows = 128-uint32 signature, 48 h TTL).
func NewMinHashIndex(c *Client, cfg MinHashIndexConfig) *MinHashIndex {
	if cfg.Bands == 0 {
		cfg.Bands = 16
	}
	if cfg.RowsPerBand == 0 {
		cfg.RowsPerBand = 8
	}
	if cfg.TTLSecs == 0 {
		cfg.TTLSecs = 48 * 3600
	}
	return &MinHashIndex{c: c, cfg: cfg}
}

// Add inserts postID into the band buckets derived from sig. The signature
// length must equal Bands*RowsPerBand — mismatches are rejected so callers
// notice misconfiguration immediately rather than silently degrading recall.
func (m *MinHashIndex) Add(ctx context.Context, postID int64, sig []uint32) error {
	expected := m.cfg.Bands * m.cfg.RowsPerBand
	if len(sig) != expected {
		return fmt.Errorf("signature size %d != bands*rows %d", len(sig), expected)
	}
	pipe := m.c.RDB().Pipeline()
	ttl := time.Duration(m.cfg.TTLSecs) * time.Second
	for b := 0; b < m.cfg.Bands; b++ {
		key := m.bandKey(b, sig[b*m.cfg.RowsPerBand:(b+1)*m.cfg.RowsPerBand])
		pipe.SAdd(ctx, key, postID)
		pipe.Expire(ctx, key, ttl)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("pipeline: %w", err)
	}
	return nil
}

// Candidates returns the union of post ids found in any band bucket matched
// by sig, capped at limit. The result is deduplicated — a single post that
// collides in multiple bands appears once.
func (m *MinHashIndex) Candidates(ctx context.Context, sig []uint32, limit int) ([]int64, error) {
	expected := m.cfg.Bands * m.cfg.RowsPerBand
	if len(sig) != expected {
		return nil, fmt.Errorf("signature size %d != bands*rows %d", len(sig), expected)
	}
	seen := make(map[int64]struct{}, limit)
	for b := 0; b < m.cfg.Bands; b++ {
		key := m.bandKey(b, sig[b*m.cfg.RowsPerBand:(b+1)*m.cfg.RowsPerBand])
		ids, err := m.c.RDB().SMembers(ctx, key).Result()
		if err != nil {
			return nil, fmt.Errorf("smembers band %d: %w", b, err)
		}
		for _, idStr := range ids {
			id, err := strconv.ParseInt(idStr, 10, 64)
			if err != nil {
				// Skip corrupted entries rather than failing the whole query.
				continue
			}
			seen[id] = struct{}{}
		}
	}
	out := make([]int64, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// bandKey hashes the rows of one band into a deterministic Redis key. SHA-1
// is used purely as a fixed-width digest — no security property is required.
func (m *MinHashIndex) bandKey(band int, rows []uint32) string {
	raw, _ := json.Marshal(rows)
	sum := sha1.Sum(raw)
	return fmt.Sprintf("mh:b%d:%s", band, hex.EncodeToString(sum[:]))
}
