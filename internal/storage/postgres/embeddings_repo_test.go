//go:build integration

package postgres

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/pgvector/pgvector-go"
	"github.com/stretchr/testify/require"
)

func fillVec(n int, x float32) []float32 {
	v := make([]float32, n)
	for i := range v {
		v[i] = x
	}
	return v
}

func TestEmbeddingsRepo_StoreAndUpsert(t *testing.T) {
	dsn := os.Getenv("POSTGRES_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_DSN unset")
	}
	ctx := context.Background()
	p, err := NewPool(ctx, dsn)
	require.NoError(t, err)
	defer p.Close()
	_, err = p.Pool().Exec(ctx, "TRUNCATE post_embeddings, posts, channels RESTART IDENTITY CASCADE")
	require.NoError(t, err)

	ch := NewChannelsRepo(p)
	chID, err := ch.Upsert(ctx, ChannelInsert{TGChannelID: 1, Username: "x", Title: "X", Authority: 50})
	require.NoError(t, err)
	pr := NewPostsRepo(p)
	pid, err := pr.Insert(ctx, PostInsert{
		ChannelID: chID, TGMsgID: 1, TextClean: "a",
		TextHash: []byte("h1"), PostedAt: time.Now().UTC(),
	})
	require.NoError(t, err)

	e := NewEmbeddingsRepo(p)
	v1 := pgvector.NewVector(fillVec(1536, 0.1))
	require.NoError(t, e.Store(ctx, pid, v1, "test"))

	// Upsert path: same post_id, new vector — must not error.
	v2 := pgvector.NewVector(fillVec(1536, 0.2))
	require.NoError(t, e.Store(ctx, pid, v2, "test-v2"))

	var model string
	require.NoError(t, p.Pool().QueryRow(ctx, "SELECT model FROM post_embeddings WHERE post_id=$1", pid).Scan(&model))
	require.Equal(t, "test-v2", model)
}

func TestEmbeddingsRepo_NearestNeighbors(t *testing.T) {
	dsn := os.Getenv("POSTGRES_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_DSN unset")
	}
	ctx := context.Background()
	p, err := NewPool(ctx, dsn)
	require.NoError(t, err)
	defer p.Close()
	_, err = p.Pool().Exec(ctx, "TRUNCATE post_embeddings, posts, channels RESTART IDENTITY CASCADE")
	require.NoError(t, err)

	ch := NewChannelsRepo(p)
	chID, err := ch.Upsert(ctx, ChannelInsert{TGChannelID: 1, Username: "x", Title: "X", Authority: 50})
	require.NoError(t, err)
	pr := NewPostsRepo(p)

	id1, err := pr.Insert(ctx, PostInsert{ChannelID: chID, TGMsgID: 1, TextClean: "a", TextHash: []byte("h1"), PostedAt: time.Now().UTC()})
	require.NoError(t, err)
	id2, err := pr.Insert(ctx, PostInsert{ChannelID: chID, TGMsgID: 2, TextClean: "b", TextHash: []byte("h2"), PostedAt: time.Now().UTC()})
	require.NoError(t, err)

	e := NewEmbeddingsRepo(p)
	// v1 close to query; v2 differs in first dim — much farther.
	v1arr := fillVec(1536, 0.1)
	v2arr := fillVec(1536, 0.1)
	v2arr[0] = 0.9
	require.NoError(t, e.Store(ctx, id1, pgvector.NewVector(v1arr), "test"))
	require.NoError(t, e.Store(ctx, id2, pgvector.NewVector(v2arr), "test"))

	query := pgvector.NewVector(v1arr)
	near, err := e.NearestNeighbors(ctx, query, NearestParams{Limit: 1, MaxCosineDistance: 0.5})
	require.NoError(t, err)
	require.Len(t, near, 1)
	require.Equal(t, id1, near[0].PostID)
	require.InDelta(t, 0.0, near[0].Distance, 1e-5)

	// Wider threshold returns both, id1 first.
	near2, err := e.NearestNeighbors(ctx, query, NearestParams{Limit: 5, MaxCosineDistance: 1.0})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(near2), 1)
	require.Equal(t, id1, near2[0].PostID)
}
