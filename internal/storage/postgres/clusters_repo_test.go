//go:build integration

package postgres

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestClustersRepo_CreateUpdateGet(t *testing.T) {
	dsn := os.Getenv("POSTGRES_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_DSN unset")
	}
	ctx := context.Background()
	p, err := NewPool(ctx, dsn)
	require.NoError(t, err)
	defer p.Close()
	_, err = p.Pool().Exec(ctx, "TRUNCATE clusters RESTART IDENTITY CASCADE")
	require.NoError(t, err)

	r := NewClustersRepo(p)
	id, err := r.Create(ctx)
	require.NoError(t, err)
	require.Greater(t, id, int64(0))

	require.NoError(t, r.UpdateMeta(ctx, id, ClusterMeta{
		Headline: "Big news",
		Summary:  "Something happened.",
		Topics:   []string{"politics"},
		Severity: 70,
	}))
	require.NoError(t, r.SetScore(ctx, id, 1.23))
	require.NoError(t, r.IncrementCoverage(ctx, id))

	c, err := r.Get(ctx, id)
	require.NoError(t, err)
	require.Equal(t, "Big news", c.Headline)
	require.Equal(t, []string{"politics"}, c.Topics)
	require.Equal(t, 70, c.Severity)
	require.InDelta(t, 1.23, c.Score, 0.001)
	require.Equal(t, 2, c.Coverage)
	require.Equal(t, "active", c.Status)
}

func TestClustersRepo_Search(t *testing.T) {
	dsn := os.Getenv("POSTGRES_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_DSN unset")
	}
	ctx := context.Background()
	p, err := NewPool(ctx, dsn)
	require.NoError(t, err)
	defer p.Close()
	_, err = p.Pool().Exec(ctx, "TRUNCATE clusters RESTART IDENTITY CASCADE")
	require.NoError(t, err)

	r := NewClustersRepo(p)
	idMatch, err := r.Create(ctx)
	require.NoError(t, err)
	require.NoError(t, r.UpdateMeta(ctx, idMatch, ClusterMeta{
		Headline: "Match", Summary: "x", Topics: []string{"politics", "war"}, Severity: 80,
	}))
	require.NoError(t, r.SetScore(ctx, idMatch, 0.9))

	idLowScore, err := r.Create(ctx)
	require.NoError(t, err)
	require.NoError(t, r.UpdateMeta(ctx, idLowScore, ClusterMeta{
		Topics: []string{"politics"}, Severity: 30,
	}))
	require.NoError(t, r.SetScore(ctx, idLowScore, 0.1))

	idWrongTopic, err := r.Create(ctx)
	require.NoError(t, err)
	require.NoError(t, r.UpdateMeta(ctx, idWrongTopic, ClusterMeta{
		Topics: []string{"sport"}, Severity: 90,
	}))
	require.NoError(t, r.SetScore(ctx, idWrongTopic, 0.95))

	idExcluded, err := r.Create(ctx)
	require.NoError(t, err)
	require.NoError(t, r.UpdateMeta(ctx, idExcluded, ClusterMeta{
		Topics: []string{"politics"}, Severity: 80,
	}))
	require.NoError(t, r.SetScore(ctx, idExcluded, 0.99))

	got, err := r.Search(ctx, ClusterFilter{
		Topics:     []string{"politics"},
		MinScore:   0.5,
		SinceTime:  time.Now().Add(-time.Hour),
		ExcludeIDs: []int64{idExcluded},
		Limit:      10,
	})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, idMatch, got[0].ID)
}
