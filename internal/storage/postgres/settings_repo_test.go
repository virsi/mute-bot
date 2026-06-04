//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSettingsRepo_Defaults(t *testing.T) {
	ctx := context.Background()
	p := setupTestPool(t)
	truncate(t, p, "user_settings, users")

	ur := NewUsersRepo(p)
	sr := NewSettingsRepo(p)
	u, _, err := ur.GetOrCreate(ctx, 7777, "carol")
	require.NoError(t, err)

	sched, err := json.Marshal(map[string]any{"times": []string{"08:00"}, "tz": "Europe/Moscow"})
	require.NoError(t, err)
	require.NoError(t, sr.Upsert(ctx, u.ID, SettingsUpdate{
		Topics:       []string{"politics"},
		Threshold:    70,
		ScheduleJSON: sched,
	}))

	s, err := sr.Get(ctx, u.ID)
	require.NoError(t, err)
	require.Equal(t, []string{"politics"}, s.Topics)
	require.Equal(t, 70, s.Threshold)
	require.False(t, s.AlertsEnabled)
	require.Equal(t, 85, s.AlertThreshold)  // default from migration 0001
	require.Equal(t, 30, s.AlertThrottleMin) // default from migration 0005
}

func TestSettingsRepo_UpsertReplaces(t *testing.T) {
	ctx := context.Background()
	p := setupTestPool(t)
	truncate(t, p, "user_settings, users")

	ur := NewUsersRepo(p)
	sr := NewSettingsRepo(p)
	u, _, err := ur.GetOrCreate(ctx, 7778, "dave")
	require.NoError(t, err)

	require.NoError(t, sr.Upsert(ctx, u.ID, SettingsUpdate{
		Topics:    []string{"it"},
		Threshold: 40,
	}))
	require.NoError(t, sr.Upsert(ctx, u.ID, SettingsUpdate{
		Topics:           []string{"crypto"},
		Threshold:        60,
		AlertsEnabled:    true,
		AlertThreshold:   92,
		AlertThrottleMin: 15,
	}))

	s, err := sr.Get(ctx, u.ID)
	require.NoError(t, err)
	require.Equal(t, []string{"crypto"}, s.Topics)
	require.Equal(t, 60, s.Threshold)
	require.True(t, s.AlertsEnabled)
	require.Equal(t, 92, s.AlertThreshold)
	require.Equal(t, 15, s.AlertThrottleMin)
}
