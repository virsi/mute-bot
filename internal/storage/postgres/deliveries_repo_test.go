//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDeliveriesRepo_RecordIdempotent(t *testing.T) {
	ctx := context.Background()
	p := setupTestPool(t)
	truncate(t, p, "deliveries, users, clusters, channels")

	ur := NewUsersRepo(p)
	cr := NewClustersRepo(p)
	dr := NewDeliveriesRepo(p)

	u, _, err := ur.GetOrCreate(ctx, 9001, "alice")
	require.NoError(t, err)
	cid, err := cr.Create(ctx)
	require.NoError(t, err)

	require.NoError(t, dr.Record(ctx, u.ID, cid, "digest"))
	require.NoError(t, dr.Record(ctx, u.ID, cid, "digest")) // second call must NOT raise

	ids, err := dr.ListClusterIDs(ctx, u.ID, 10)
	require.NoError(t, err)
	require.Equal(t, []int64{cid}, ids)
}

func TestDeliveriesRepo_ListOrder(t *testing.T) {
	ctx := context.Background()
	p := setupTestPool(t)
	truncate(t, p, "deliveries, users, clusters")

	ur := NewUsersRepo(p)
	cr := NewClustersRepo(p)
	dr := NewDeliveriesRepo(p)

	u, _, err := ur.GetOrCreate(ctx, 9002, "bob")
	require.NoError(t, err)

	cids := make([]int64, 0, 3)
	for i := 0; i < 3; i++ {
		c, err := cr.Create(ctx)
		require.NoError(t, err)
		cids = append(cids, c)
		require.NoError(t, dr.Record(ctx, u.ID, c, "digest"))
		time.Sleep(10 * time.Millisecond) // ensure distinct delivered_at
	}

	ids, err := dr.ListClusterIDs(ctx, u.ID, 10)
	require.NoError(t, err)
	require.Equal(t, []int64{cids[2], cids[1], cids[0]}, ids) // newest first
}
