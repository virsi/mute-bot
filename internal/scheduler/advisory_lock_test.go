//go:build integration

package scheduler

import (
	"context"
	"hash/fnv"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/virsi/mute-bot/internal/storage/postgres"
)

// TestAdvisoryLock_AcquireRelease exercises advisory locks from two
// independent pools to simulate two scheduler replicas contending for the
// same key. We need separate pools because pg_try_advisory_lock is
// session-scoped *and* re-entrant: the same session can re-acquire its
// own lock, so within a single pool the second TryLock would always
// succeed (the pgxpool LIFO returns the same conn).
func TestAdvisoryLock_AcquireRelease(t *testing.T) {
	dsn := os.Getenv("POSTGRES_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_DSN unset")
	}
	ctx := context.Background()

	holder, err := postgres.NewPool(ctx, dsn)
	require.NoError(t, err)
	defer holder.Close()

	contender, err := postgres.NewPool(ctx, dsn)
	require.NoError(t, err)
	defer contender.Close()

	key := hashKey("digest:morning")

	ok, err := TryLock(ctx, holder, key)
	require.NoError(t, err)
	require.True(t, ok)

	ok2, err := TryLock(ctx, contender, key)
	require.NoError(t, err)
	require.False(t, ok2, "contender must not steal the lock from holder")

	require.NoError(t, Unlock(ctx, holder, key))

	ok3, err := TryLock(ctx, contender, key)
	require.NoError(t, err)
	require.True(t, ok3, "contender must acquire the lock once holder releases")
	require.NoError(t, Unlock(ctx, contender, key))
}

func hashKey(s string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	return int64(h.Sum64())
}
