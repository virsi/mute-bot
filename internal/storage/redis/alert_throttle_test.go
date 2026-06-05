//go:build integration

package redis

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAlertThrottle_FirstAcquireSucceedsSecondDenied(t *testing.T) {
	c, cleanup := setupTestRedis(t)
	defer cleanup()
	thr := NewAlertThrottle(c)
	ctx := context.Background()

	ok, err := thr.Allow(ctx, 42, "war", 30*time.Second)
	require.NoError(t, err)
	require.True(t, ok, "first attempt must acquire the throttle slot")

	ok2, err := thr.Allow(ctx, 42, "war", 30*time.Second)
	require.NoError(t, err)
	require.False(t, ok2, "second attempt within TTL must be denied")
}

func TestAlertThrottle_DifferentUsersOrTopicsIndependent(t *testing.T) {
	c, cleanup := setupTestRedis(t)
	defer cleanup()
	thr := NewAlertThrottle(c)
	ctx := context.Background()

	ok1, err := thr.Allow(ctx, 1, "war", 30*time.Second)
	require.NoError(t, err)
	require.True(t, ok1)

	// Different user — independent key.
	ok2, err := thr.Allow(ctx, 2, "war", 30*time.Second)
	require.NoError(t, err)
	require.True(t, ok2)

	// Same user but different topic — independent key.
	ok3, err := thr.Allow(ctx, 1, "politics", 30*time.Second)
	require.NoError(t, err)
	require.True(t, ok3)
}

func TestAlertThrottle_ExpiresAfterTTL(t *testing.T) {
	c, cleanup := setupTestRedis(t)
	defer cleanup()
	thr := NewAlertThrottle(c)
	ctx := context.Background()

	ok, err := thr.Allow(ctx, 99, "war", 250*time.Millisecond)
	require.NoError(t, err)
	require.True(t, ok)

	// Within TTL — denied.
	ok2, err := thr.Allow(ctx, 99, "war", 250*time.Millisecond)
	require.NoError(t, err)
	require.False(t, ok2)

	// After TTL — slot frees up.
	time.Sleep(400 * time.Millisecond)
	ok3, err := thr.Allow(ctx, 99, "war", 250*time.Millisecond)
	require.NoError(t, err)
	require.True(t, ok3, "throttle must release after TTL expires")
}
