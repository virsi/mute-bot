//go:build integration

package postgres

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestChannelsRepo_UpsertAndGet(t *testing.T) {
	dsn := os.Getenv("POSTGRES_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_DSN unset")
	}
	ctx := context.Background()
	p, err := NewPool(ctx, dsn)
	require.NoError(t, err)
	defer p.Close()
	_, err = p.Pool().Exec(ctx, "TRUNCATE channels RESTART IDENTITY CASCADE")
	require.NoError(t, err)

	r := NewChannelsRepo(p)
	id, err := r.Upsert(ctx, ChannelInsert{TGChannelID: 42, Username: "ria", Title: "RIA", Authority: 80})
	require.NoError(t, err)
	require.Greater(t, id, int64(0))

	got, err := r.GetByTGID(ctx, 42)
	require.NoError(t, err)
	require.Equal(t, "ria", got.Username)
	require.Equal(t, 80, got.Authority)
	require.True(t, got.Active)

	id2, err := r.Upsert(ctx, ChannelInsert{TGChannelID: 42, Username: "ria_novosti", Title: "RIA", Authority: 85})
	require.NoError(t, err)
	require.Equal(t, id, id2)

	got2, err := r.GetByTGID(ctx, 42)
	require.NoError(t, err)
	require.Equal(t, "ria_novosti", got2.Username)
	require.Equal(t, 85, got2.Authority)
}

func TestChannelsRepo_ResolveOrCreate(t *testing.T) {
	dsn := os.Getenv("POSTGRES_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_DSN unset")
	}
	ctx := context.Background()
	p, err := NewPool(ctx, dsn)
	require.NoError(t, err)
	defer p.Close()
	_, err = p.Pool().Exec(ctx, "TRUNCATE channels RESTART IDENTITY CASCADE")
	require.NoError(t, err)

	r := NewChannelsRepo(p)

	id1, err := r.ResolveOrCreate(ctx, 555)
	require.NoError(t, err)
	require.Greater(t, id1, int64(0))

	// Same tg_channel_id → same internal id (idempotent).
	id2, err := r.ResolveOrCreate(ctx, 555)
	require.NoError(t, err)
	require.Equal(t, id1, id2)

	// A different tg_channel_id → a fresh internal id.
	id3, err := r.ResolveOrCreate(ctx, 777)
	require.NoError(t, err)
	require.NotEqual(t, id1, id3)

	// ResolveOrCreate must not overwrite previously-set username/title/authority.
	_, err = r.Upsert(ctx, ChannelInsert{TGChannelID: 555, Username: "ria", Title: "RIA", Authority: 80})
	require.NoError(t, err)
	id4, err := r.ResolveOrCreate(ctx, 555)
	require.NoError(t, err)
	require.Equal(t, id1, id4)
	got, err := r.GetByTGID(ctx, 555)
	require.NoError(t, err)
	require.Equal(t, "ria", got.Username)
	require.Equal(t, 80, got.Authority)
}

func TestChannelsRepo_ListActive(t *testing.T) {
	dsn := os.Getenv("POSTGRES_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_DSN unset")
	}
	ctx := context.Background()
	p, err := NewPool(ctx, dsn)
	require.NoError(t, err)
	defer p.Close()
	_, err = p.Pool().Exec(ctx, "TRUNCATE channels RESTART IDENTITY CASCADE")
	require.NoError(t, err)

	r := NewChannelsRepo(p)
	_, err = r.Upsert(ctx, ChannelInsert{TGChannelID: 1, Username: "a", Title: "A", Authority: 50})
	require.NoError(t, err)
	_, err = r.Upsert(ctx, ChannelInsert{TGChannelID: 2, Username: "b", Title: "B", Authority: 60})
	require.NoError(t, err)
	_, err = p.Pool().Exec(ctx, "UPDATE channels SET active = false WHERE tg_channel_id = 2")
	require.NoError(t, err)

	got, err := r.ListActive(ctx)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.EqualValues(t, 1, got[0].TGChannelID)
}
