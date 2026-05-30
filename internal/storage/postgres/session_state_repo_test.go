//go:build integration

package postgres

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSessionStateRepo_UpsertAndGet(t *testing.T) {
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

	ch := NewChannelsRepo(p)
	chID, err := ch.Upsert(ctx, ChannelInsert{TGChannelID: 99, Username: "x", Title: "X", Authority: 50})
	require.NoError(t, err)

	r := NewSessionStateRepo(p)

	// Zero by default — no row yet.
	got, err := r.GetLastMsgID(ctx, chID)
	require.NoError(t, err)
	require.Equal(t, int64(0), got)

	// Insert.
	require.NoError(t, r.UpsertLastMsgID(ctx, chID, 123))
	got, err = r.GetLastMsgID(ctx, chID)
	require.NoError(t, err)
	require.Equal(t, int64(123), got)

	// Update upwards.
	require.NoError(t, r.UpsertLastMsgID(ctx, chID, 200))
	got, err = r.GetLastMsgID(ctx, chID)
	require.NoError(t, err)
	require.Equal(t, int64(200), got)

	// Update with a lower id must NOT regress (GREATEST).
	require.NoError(t, r.UpsertLastMsgID(ctx, chID, 150))
	got, err = r.GetLastMsgID(ctx, chID)
	require.NoError(t, err)
	require.Equal(t, int64(200), got)
}
