//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestWeeklyDeliveriesRepo_InsertIfAbsent_FirstWins(t *testing.T) {
	ctx := context.Background()
	p := setupTestPool(t)
	truncate(t, p, "weekly_deliveries, deliveries, users, clusters")

	ur := NewUsersRepo(p)
	cr := NewClustersRepo(p)
	wr := NewWeeklyDeliveriesRepo(p)

	u, _, err := ur.GetOrCreate(ctx, 9101, "alice")
	require.NoError(t, err)
	c1, err := cr.Create(ctx)
	require.NoError(t, err)

	inserted, err := wr.InsertIfAbsent(ctx, u.ID, c1, "2026-23")
	require.NoError(t, err)
	require.True(t, inserted)

	inserted2, err := wr.InsertIfAbsent(ctx, u.ID, c1, "2026-23")
	require.NoError(t, err)
	require.False(t, inserted2)
}

func TestWeeklyDeliveriesRepo_OncePerWeek(t *testing.T) {
	ctx := context.Background()
	p := setupTestPool(t)
	truncate(t, p, "weekly_deliveries, deliveries, users, clusters")

	ur := NewUsersRepo(p)
	cr := NewClustersRepo(p)
	wr := NewWeeklyDeliveriesRepo(p)

	u, _, err := ur.GetOrCreate(ctx, 9102, "bob")
	require.NoError(t, err)
	c1, err := cr.Create(ctx)
	require.NoError(t, err)
	c2, err := cr.Create(ctx)
	require.NoError(t, err)

	// No row in this week yet.
	ok, err := wr.HasWeekRow(ctx, u.ID, "2026-23")
	require.NoError(t, err)
	require.False(t, ok)

	inserted, err := wr.InsertIfAbsent(ctx, u.ID, c1, "2026-23")
	require.NoError(t, err)
	require.True(t, inserted)

	// Second cluster in same week: anti-repeat is per (user, cluster, week),
	// not per (user, week) — both inserts succeed. The "one weekly per
	// user per week" semantic is enforced at the cron level via
	// HasWeekRow.
	inserted, err = wr.InsertIfAbsent(ctx, u.ID, c2, "2026-23")
	require.NoError(t, err)
	require.True(t, inserted)

	ok, err = wr.HasWeekRow(ctx, u.ID, "2026-23")
	require.NoError(t, err)
	require.True(t, ok)
}

func TestWeeklyDeliveriesRepo_ListClusterIDsSince_NewestFirst(t *testing.T) {
	ctx := context.Background()
	p := setupTestPool(t)
	truncate(t, p, "weekly_deliveries, deliveries, users, clusters")

	ur := NewUsersRepo(p)
	cr := NewClustersRepo(p)
	wr := NewWeeklyDeliveriesRepo(p)
	u, _, err := ur.GetOrCreate(ctx, 9103, "carol")
	require.NoError(t, err)

	var got []int64
	for i := 0; i < 3; i++ {
		cid, err := cr.Create(ctx)
		require.NoError(t, err)
		_, err = wr.InsertIfAbsent(ctx, u.ID, cid, "2026-22")
		require.NoError(t, err)
		got = append(got, cid)
		time.Sleep(5 * time.Millisecond)
	}
	ids, err := wr.ListClusterIDsSince(ctx, u.ID, time.Now().Add(-1*time.Hour), 10)
	require.NoError(t, err)
	require.Len(t, ids, 3)
	require.Equal(t, got[2], ids[0]) // newest first
}

func TestWeeklyDeliveriesRepo_ListClusterIDsSince_RespectsCutoff(t *testing.T) {
	ctx := context.Background()
	p := setupTestPool(t)
	truncate(t, p, "weekly_deliveries, deliveries, users, clusters")

	ur := NewUsersRepo(p)
	cr := NewClustersRepo(p)
	wr := NewWeeklyDeliveriesRepo(p)
	u, _, err := ur.GetOrCreate(ctx, 9104, "dave")
	require.NoError(t, err)

	cid, err := cr.Create(ctx)
	require.NoError(t, err)
	_, err = wr.InsertIfAbsent(ctx, u.ID, cid, "2026-22")
	require.NoError(t, err)

	// Cutoff in the future: nothing matches.
	ids, err := wr.ListClusterIDsSince(ctx, u.ID, time.Now().Add(time.Hour), 10)
	require.NoError(t, err)
	require.Empty(t, ids)
}
