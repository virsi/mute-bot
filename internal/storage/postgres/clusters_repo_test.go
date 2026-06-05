//go:build integration

package postgres

import (
	"context"
	"fmt"
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

func TestClustersRepo_TopByScoreSince(t *testing.T) {
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

	cr := NewClustersRepo(p)
	ids := make([]int64, 3)
	scores := []float32{0.5, 0.9, 0.7}
	for i := 0; i < 3; i++ {
		id, err := cr.Create(ctx)
		require.NoError(t, err)
		require.NoError(t, cr.UpdateMeta(ctx, id, ClusterMeta{
			Headline: fmt.Sprintf("h%d", i),
			Summary:  fmt.Sprintf("s%d", i),
			Topics:   []string{"politics"},
			Severity: 5,
		}))
		require.NoError(t, cr.SetScore(ctx, id, scores[i]))
		ids[i] = id
	}
	out, err := cr.TopByScoreSince(ctx, time.Now().Add(-1*time.Hour),
		[]string{"politics"}, []int64{}, 10)
	require.NoError(t, err)
	require.Len(t, out, 3)
	require.Equal(t, ids[1], out[0].ID) // 0.9
	require.Equal(t, ids[2], out[1].ID) // 0.7
	require.Equal(t, ids[0], out[2].ID) // 0.5
}

func TestClustersRepo_TopByScoreSince_EmptyTopics_AllPass(t *testing.T) {
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

	cr := NewClustersRepo(p)
	id, err := cr.Create(ctx)
	require.NoError(t, err)
	require.NoError(t, cr.UpdateMeta(ctx, id, ClusterMeta{
		Topics: []string{"sports"}, Severity: 3,
	}))
	require.NoError(t, cr.SetScore(ctx, id, 0.5))

	out, err := cr.TopByScoreSince(ctx, time.Now().Add(-1*time.Hour), nil, nil, 10)
	require.NoError(t, err)
	require.Len(t, out, 1)
}

func TestClustersRepo_TopByScoreSince_ExcludesIDs(t *testing.T) {
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

	cr := NewClustersRepo(p)
	id1, err := cr.Create(ctx)
	require.NoError(t, err)
	require.NoError(t, cr.UpdateMeta(ctx, id1, ClusterMeta{
		Topics: []string{"politics"}, Severity: 5,
	}))
	require.NoError(t, cr.SetScore(ctx, id1, 0.9))

	id2, err := cr.Create(ctx)
	require.NoError(t, err)
	require.NoError(t, cr.UpdateMeta(ctx, id2, ClusterMeta{
		Topics: []string{"politics"}, Severity: 5,
	}))
	require.NoError(t, cr.SetScore(ctx, id2, 0.7))

	out, err := cr.TopByScoreSince(ctx, time.Now().Add(-1*time.Hour),
		[]string{"politics"}, []int64{id1}, 10)
	require.NoError(t, err)
	require.Len(t, out, 1)
	require.Equal(t, id2, out[0].ID)
}
