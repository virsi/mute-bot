//go:build integration

package redis

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func setupTestRedis(t *testing.T) (*Client, func()) {
	t.Helper()
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := NewClient(ctx, addr)
	require.NoError(t, err)
	// Wipe leftover dedup:borderline list so tests don't see each other.
	require.NoError(t, c.RDB().Del(ctx, "dedup:borderline").Err())
	return c, func() { _ = c.Close() }
}

func TestBorderlineQueue_PushDrain(t *testing.T) {
	c, cleanup := setupTestRedis(t)
	defer cleanup()
	q := NewBorderlineQueue(c, 10)
	ctx := context.Background()
	for i := int64(1); i <= 3; i++ {
		require.NoError(t, q.Push(ctx, BorderlinePair{PostID: i, CandidateID: i + 100, Distance: 0.2}))
	}
	pairs, err := q.Drain(ctx, 10)
	require.NoError(t, err)
	require.Len(t, pairs, 3)
	require.Equal(t, int64(1), pairs[0].PostID)
}

func TestBorderlineQueue_TrimsToMax(t *testing.T) {
	c, cleanup := setupTestRedis(t)
	defer cleanup()
	q := NewBorderlineQueue(c, 5)
	ctx := context.Background()
	for i := int64(1); i <= 10; i++ {
		require.NoError(t, q.Push(ctx, BorderlinePair{PostID: i, CandidateID: 0, Distance: 0.2}))
	}
	pairs, err := q.Drain(ctx, 100)
	require.NoError(t, err)
	require.Len(t, pairs, 5)
	require.Equal(t, int64(6), pairs[0].PostID) // tail kept
}
