// Package redis wraps the go-redis client and adds dedup-specific
// data structures (MinHash LSH index, embedding cache) used by the
// processor binary.
package redis

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
)

// Client is a thin wrapper around *redis.Client that owns the connection
// lifecycle. Callers use RDB() to issue raw commands.
type Client struct {
	rdb *redis.Client
}

// NewClient dials addr and verifies the connection with PING. It returns an
// error if the server is unreachable so wiring fails fast at startup.
func NewClient(ctx context.Context, addr string) (*Client, error) {
	rdb := redis.NewClient(&redis.Options{Addr: addr})
	if err := rdb.Ping(ctx).Err(); err != nil {
		_ = rdb.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	return &Client{rdb: rdb}, nil
}

// RDB exposes the underlying *redis.Client for use by the index/cache types
// in this package.
func (c *Client) RDB() *redis.Client { return c.rdb }

// Close releases the connection pool.
func (c *Client) Close() error { return c.rdb.Close() }
