//go:build integration

package postgres

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPostsRepo_InsertIsIdempotent(t *testing.T) {
	dsn := os.Getenv("POSTGRES_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_DSN unset")
	}
	ctx := context.Background()
	p, err := NewPool(ctx, dsn)
	require.NoError(t, err)
	defer p.Close()
	_, err = p.Pool().Exec(ctx, "TRUNCATE posts, channels RESTART IDENTITY CASCADE")
	require.NoError(t, err)

	chRepo := NewChannelsRepo(p)
	chID, err := chRepo.Upsert(ctx, ChannelInsert{TGChannelID: 1, Username: "x", Title: "X", Authority: 50})
	require.NoError(t, err)

	pr := NewPostsRepo(p)
	id, err := pr.Insert(ctx, PostInsert{
		ChannelID: chID, TGMsgID: 10,
		TextRaw: "Foo bar", TextClean: "foo bar",
		TextHash: []byte("hash01"), Lang: "en",
		PostedAt: time.Now().UTC(),
	})
	require.NoError(t, err)
	require.Greater(t, id, int64(0))

	// Re-insert with same (channel_id, tg_msg_id) → returns same id.
	id2, err := pr.Insert(ctx, PostInsert{
		ChannelID: chID, TGMsgID: 10,
		TextRaw: "Foo bar", TextClean: "foo bar",
		TextHash: []byte("hash01"), Lang: "en",
		PostedAt: time.Now().UTC(),
	})
	require.NoError(t, err)
	require.Equal(t, id, id2)
}

func TestPostsRepo_AttachClusterAndList(t *testing.T) {
	dsn := os.Getenv("POSTGRES_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_DSN unset")
	}
	ctx := context.Background()
	p, err := NewPool(ctx, dsn)
	require.NoError(t, err)
	defer p.Close()
	_, err = p.Pool().Exec(ctx, "TRUNCATE posts, clusters, channels RESTART IDENTITY CASCADE")
	require.NoError(t, err)

	chRepo := NewChannelsRepo(p)
	chID, err := chRepo.Upsert(ctx, ChannelInsert{TGChannelID: 1, Username: "x", Title: "X", Authority: 50})
	require.NoError(t, err)

	// Create a cluster row directly (ClustersRepo arrives in next task).
	var cid int64
	require.NoError(t, p.Pool().QueryRow(ctx, "INSERT INTO clusters DEFAULT VALUES RETURNING id").Scan(&cid))

	pr := NewPostsRepo(p)
	postID, err := pr.Insert(ctx, PostInsert{
		ChannelID: chID, TGMsgID: 11,
		TextRaw: "raw", TextClean: "clean",
		TextHash: []byte("h"), Lang: "ru",
		PostedAt: time.Now().UTC(),
	})
	require.NoError(t, err)

	require.NoError(t, pr.AttachCluster(ctx, postID, cid))

	posts, err := pr.ListByCluster(ctx, cid)
	require.NoError(t, err)
	require.Len(t, posts, 1)
	require.Equal(t, postID, posts[0].ID)
	require.NotNil(t, posts[0].ClusterID)
	require.Equal(t, cid, *posts[0].ClusterID)
}
