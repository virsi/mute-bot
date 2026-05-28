//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUsersRepo_GetOrCreate(t *testing.T) {
	dsn := os.Getenv("POSTGRES_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_DSN unset")
	}
	ctx := context.Background()
	p, err := NewPool(ctx, dsn)
	require.NoError(t, err)
	defer p.Close()
	_, err = p.Pool().Exec(ctx, "TRUNCATE users RESTART IDENTITY CASCADE")
	require.NoError(t, err)

	r := NewUsersRepo(p)
	u1, created, err := r.GetOrCreate(ctx, 555, "virsi")
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, "free", u1.Tier)
	require.Equal(t, "virsi", u1.Username)

	u2, created2, err := r.GetOrCreate(ctx, 555, "virsi_changed")
	require.NoError(t, err)
	require.False(t, created2)
	require.Equal(t, u1.ID, u2.ID)
	require.Equal(t, "virsi_changed", u2.Username)

	require.NoError(t, r.SetBlocked(ctx, u1.ID, true))
	u3, _, err := r.GetOrCreate(ctx, 555, "virsi_changed")
	require.NoError(t, err)
	require.True(t, u3.Blocked)
}

func TestSettingsRepo_UpsertGet(t *testing.T) {
	dsn := os.Getenv("POSTGRES_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_DSN unset")
	}
	ctx := context.Background()
	p, err := NewPool(ctx, dsn)
	require.NoError(t, err)
	defer p.Close()
	_, err = p.Pool().Exec(ctx, "TRUNCATE users RESTART IDENTITY CASCADE")
	require.NoError(t, err)

	ur := NewUsersRepo(p)
	u, _, err := ur.GetOrCreate(ctx, 777, "alice")
	require.NoError(t, err)

	r := NewSettingsRepo(p)
	sched, err := json.Marshal(map[string]any{"times": []string{"09:00"}, "tz": "Europe/Moscow"})
	require.NoError(t, err)
	require.NoError(t, r.Upsert(ctx, u.ID, SettingsUpdate{
		Topics:       []string{"politics", "it"},
		Threshold:    60,
		ScheduleJSON: sched,
	}))

	s, err := r.Get(ctx, u.ID)
	require.NoError(t, err)
	require.Equal(t, []string{"politics", "it"}, s.Topics)
	require.Equal(t, 60, s.Threshold)
	require.JSONEq(t, string(sched), string(s.ScheduleJSON))

	// Re-upsert overwrites.
	require.NoError(t, r.Upsert(ctx, u.ID, SettingsUpdate{
		Topics:         []string{"war"},
		Threshold:      75,
		ScheduleJSON:   sched,
		AlertsEnabled:  true,
		AlertThreshold: 90,
		WeeklyEnabled:  true,
	}))
	s2, err := r.Get(ctx, u.ID)
	require.NoError(t, err)
	require.Equal(t, []string{"war"}, s2.Topics)
	require.Equal(t, 75, s2.Threshold)
	require.True(t, s2.AlertsEnabled)
	require.Equal(t, 90, s2.AlertThreshold)
	require.True(t, s2.WeeklyEnabled)
}

func TestDeliveriesRepo_RecordAndList(t *testing.T) {
	dsn := os.Getenv("POSTGRES_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_DSN unset")
	}
	ctx := context.Background()
	p, err := NewPool(ctx, dsn)
	require.NoError(t, err)
	defer p.Close()
	_, err = p.Pool().Exec(ctx, "TRUNCATE users, clusters, deliveries RESTART IDENTITY CASCADE")
	require.NoError(t, err)

	ur := NewUsersRepo(p)
	cr := NewClustersRepo(p)
	dr := NewDeliveriesRepo(p)

	u, _, err := ur.GetOrCreate(ctx, 1, "u")
	require.NoError(t, err)
	cid, err := cr.Create(ctx)
	require.NoError(t, err)

	require.NoError(t, dr.Record(ctx, u.ID, cid, "digest"))
	require.NoError(t, dr.Record(ctx, u.ID, cid, "digest")) // idempotent

	ids, err := dr.ListClusterIDs(ctx, u.ID, 100)
	require.NoError(t, err)
	require.Equal(t, []int64{cid}, ids)
}
