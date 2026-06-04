//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

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

func TestUsersRepo_GrantPro_NewSetsUntil(t *testing.T) {
	p := setupTestPool(t)
	truncate(t, p, "users")
	ctx := context.Background()
	r := NewUsersRepo(p)
	u, _, err := r.GetOrCreate(ctx, 1111, "u")
	require.NoError(t, err)

	require.NoError(t, r.GrantPro(ctx, u.ID, 30*24*time.Hour))

	u2, _, err := r.GetOrCreate(ctx, 1111, "u")
	require.NoError(t, err)
	require.Equal(t, "pro", u2.Tier)
	require.NotNil(t, u2.TierUntil)
	require.True(t, u2.TierUntil.After(time.Now().Add(29*24*time.Hour)))
}

func TestUsersRepo_GrantPro_Extends(t *testing.T) {
	p := setupTestPool(t)
	truncate(t, p, "users")
	ctx := context.Background()
	r := NewUsersRepo(p)
	u, _, err := r.GetOrCreate(ctx, 2222, "u")
	require.NoError(t, err)

	require.NoError(t, r.GrantPro(ctx, u.ID, 30*24*time.Hour))
	u2, _, err := r.GetOrCreate(ctx, 2222, "u")
	require.NoError(t, err)
	require.NotNil(t, u2.TierUntil)
	first := *u2.TierUntil

	require.NoError(t, r.GrantPro(ctx, u.ID, 30*24*time.Hour))
	u3, _, err := r.GetOrCreate(ctx, 2222, "u")
	require.NoError(t, err)
	require.NotNil(t, u3.TierUntil)
	// Second grant adds another 30 days on top of the first deadline.
	require.True(t, u3.TierUntil.After(first.Add(29*24*time.Hour)),
		"second grant must extend, got first=%v second=%v", first, *u3.TierUntil)
}

func TestUsersRepo_ListExpired(t *testing.T) {
	p := setupTestPool(t)
	truncate(t, p, "users")
	ctx := context.Background()
	r := NewUsersRepo(p)

	u1, _, err := r.GetOrCreate(ctx, 3333, "u")
	require.NoError(t, err)
	_, err = p.Pool().Exec(ctx,
		`UPDATE users SET tier='pro', tier_until=now()-interval '1 hour' WHERE id=$1`, u1.ID)
	require.NoError(t, err)

	u2, _, err := r.GetOrCreate(ctx, 3334, "v")
	require.NoError(t, err)
	_, err = p.Pool().Exec(ctx,
		`UPDATE users SET tier='pro', tier_until=now()+interval '1 hour' WHERE id=$1`, u2.ID)
	require.NoError(t, err)

	// Free user must not show up regardless of tier_until.
	u3, _, err := r.GetOrCreate(ctx, 3335, "w")
	require.NoError(t, err)
	_, err = p.Pool().Exec(ctx,
		`UPDATE users SET tier_until=now()-interval '1 hour' WHERE id=$1`, u3.ID)
	require.NoError(t, err)

	ids, err := r.ListExpired(ctx, time.Now())
	require.NoError(t, err)
	require.Equal(t, []int64{u1.ID}, ids)
}

func TestUsersRepo_GetByID(t *testing.T) {
	p := setupTestPool(t)
	truncate(t, p, "users")
	ctx := context.Background()
	r := NewUsersRepo(p)

	u, _, err := r.GetOrCreate(ctx, 5555, "z")
	require.NoError(t, err)

	got, err := r.GetByID(ctx, u.ID)
	require.NoError(t, err)
	require.Equal(t, u.ID, got.ID)
	require.Equal(t, int64(5555), got.TGUserID)
	require.Equal(t, "free", got.Tier)

	// Pro user with a tier_until comes back with the deadline intact.
	require.NoError(t, r.GrantPro(ctx, u.ID, 30*24*time.Hour))
	pro, err := r.GetByID(ctx, u.ID)
	require.NoError(t, err)
	require.Equal(t, "pro", pro.Tier)
	require.NotNil(t, pro.TierUntil)
}

func TestUsersRepo_SetTier(t *testing.T) {
	p := setupTestPool(t)
	truncate(t, p, "users")
	ctx := context.Background()
	r := NewUsersRepo(p)
	u, _, err := r.GetOrCreate(ctx, 4444, "u")
	require.NoError(t, err)
	require.NoError(t, r.GrantPro(ctx, u.ID, 24*time.Hour))

	require.NoError(t, r.SetTier(ctx, u.ID, "free", nil))

	u2, _, err := r.GetOrCreate(ctx, 4444, "u")
	require.NoError(t, err)
	require.Equal(t, "free", u2.Tier)
	require.Nil(t, u2.TierUntil)
}

func TestUsersRepo_ListAlertEligible(t *testing.T) {
	p := setupTestPool(t)
	truncate(t, p, "users")
	ctx := context.Background()
	ur := NewUsersRepo(p)
	sr := NewSettingsRepo(p)

	// Pro + alerts_enabled — eligible.
	pro, _, err := ur.GetOrCreate(ctx, 10001, "pro")
	require.NoError(t, err)
	require.NoError(t, ur.GrantPro(ctx, pro.ID, 30*24*time.Hour))
	require.NoError(t, sr.Upsert(ctx, pro.ID, SettingsUpdate{
		Topics: []string{}, Threshold: 50,
		AlertsEnabled: true, AlertThreshold: 80, AlertThrottleMin: 20,
	}))

	// Pro but alerts_enabled = false — excluded.
	silent, _, err := ur.GetOrCreate(ctx, 10002, "silent")
	require.NoError(t, err)
	require.NoError(t, ur.GrantPro(ctx, silent.ID, 30*24*time.Hour))
	require.NoError(t, sr.Upsert(ctx, silent.ID, SettingsUpdate{
		Topics: []string{}, Threshold: 50, AlertsEnabled: false,
	}))

	// Free user with alerts_enabled = true — excluded (tier check).
	free, _, err := ur.GetOrCreate(ctx, 10003, "free")
	require.NoError(t, err)
	require.NoError(t, sr.Upsert(ctx, free.ID, SettingsUpdate{
		Topics: []string{}, Threshold: 50, AlertsEnabled: true,
	}))

	// Pro expired — excluded.
	expired, _, err := ur.GetOrCreate(ctx, 10004, "expired")
	require.NoError(t, err)
	_, err = p.Pool().Exec(ctx,
		`UPDATE users SET tier='pro', tier_until=now()-interval '1 hour' WHERE id=$1`, expired.ID)
	require.NoError(t, err)
	require.NoError(t, sr.Upsert(ctx, expired.ID, SettingsUpdate{
		Topics: []string{}, Threshold: 50, AlertsEnabled: true,
	}))

	// Pro blocked — excluded.
	blocked, _, err := ur.GetOrCreate(ctx, 10005, "blocked")
	require.NoError(t, err)
	require.NoError(t, ur.GrantPro(ctx, blocked.ID, 30*24*time.Hour))
	require.NoError(t, ur.SetBlocked(ctx, blocked.ID, true))
	require.NoError(t, sr.Upsert(ctx, blocked.ID, SettingsUpdate{
		Topics: []string{}, Threshold: 50, AlertsEnabled: true,
	}))

	out, err := ur.ListAlertEligible(ctx)
	require.NoError(t, err)
	require.Len(t, out, 1)
	require.Equal(t, pro.ID, out[0].UserID)
	require.Equal(t, int64(10001), out[0].TGUserID)
	require.Equal(t, 80, out[0].Threshold)
	require.Equal(t, 20, out[0].ThrottleMin)
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
