//go:build integration

package redis

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMinHashIndex_AddAndQuery(t *testing.T) {
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		t.Skip("REDIS_ADDR unset")
	}
	ctx := context.Background()
	c, err := NewClient(ctx, addr)
	require.NoError(t, err)
	defer func() { _ = c.Close() }()
	_, err = c.RDB().FlushDB(ctx).Result()
	require.NoError(t, err)

	idx := NewMinHashIndex(c, MinHashIndexConfig{Bands: 16, RowsPerBand: 8, TTLSecs: 3600})
	require.NoError(t, idx.Add(ctx, 1, dummySig(128, 100)))
	require.NoError(t, idx.Add(ctx, 2, dummySig(128, 100))) // identical
	require.NoError(t, idx.Add(ctx, 3, dummySig(128, 200))) // different

	cands, err := idx.Candidates(ctx, dummySig(128, 100), 10)
	require.NoError(t, err)
	require.ElementsMatch(t, []int64{1, 2}, cands)
}

func TestMinHashIndex_RejectsWrongSize(t *testing.T) {
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		t.Skip("REDIS_ADDR unset")
	}
	ctx := context.Background()
	c, err := NewClient(ctx, addr)
	require.NoError(t, err)
	defer func() { _ = c.Close() }()

	idx := NewMinHashIndex(c, MinHashIndexConfig{Bands: 16, RowsPerBand: 8, TTLSecs: 60})
	err = idx.Add(ctx, 1, dummySig(64, 0))
	require.Error(t, err)
}

func dummySig(n int, seed uint32) []uint32 {
	s := make([]uint32, n)
	for i := range s {
		s[i] = seed + uint32(i)
	}
	return s
}
