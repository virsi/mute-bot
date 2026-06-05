//go:build integration

package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

// TestSubscriptionsRepo_Insert_Idempotent locks in the idempotency contract:
// repeating Insert with the same (provider, provider_ref) returns the same
// id and isNew=false on the second call. This is what makes the
// successful_payment webhook safe to retry.
func TestSubscriptionsRepo_Insert_Idempotent(t *testing.T) {
	ctx := context.Background()
	p := setupTestPool(t)
	truncate(t, p, "subscriptions, users")

	ur := NewUsersRepo(p)
	sr := NewSubscriptionsRepo(p)
	u, _, err := ur.GetOrCreate(ctx, 100, "alice")
	require.NoError(t, err)
	started := time.Now().UTC().Truncate(time.Second)
	expires := started.Add(30 * 24 * time.Hour)

	id1, isNew1, err := sr.Insert(ctx, SubscriptionInsert{
		UserID:      u.ID,
		Provider:    "tg_stars",
		ProviderRef: "charge-ref-1",
		Plan:        "pro_30d",
		StartedAt:   started,
		ExpiresAt:   &expires,
	})
	require.NoError(t, err)
	require.True(t, isNew1)
	require.Greater(t, id1, int64(0))

	id2, isNew2, err := sr.Insert(ctx, SubscriptionInsert{
		UserID:      u.ID,
		Provider:    "tg_stars",
		ProviderRef: "charge-ref-1",
		Plan:        "pro_30d",
		StartedAt:   started,
		ExpiresAt:   &expires,
	})
	require.NoError(t, err)
	require.False(t, isNew2, "second insert with same provider_ref must report isNew=false")
	require.Equal(t, id1, id2, "second insert must return the existing row id")
}

// TestSubscriptionsRepo_ListByUser checks rows come back newest first.
func TestSubscriptionsRepo_ListByUser(t *testing.T) {
	ctx := context.Background()
	p := setupTestPool(t)
	truncate(t, p, "subscriptions, users")

	ur := NewUsersRepo(p)
	sr := NewSubscriptionsRepo(p)
	u, _, err := ur.GetOrCreate(ctx, 101, "bob")
	require.NoError(t, err)

	now := time.Now().UTC().Truncate(time.Second)
	for i, ref := range []string{"r1", "r2", "r3"} {
		expires := now.Add(30 * 24 * time.Hour)
		_, _, err := sr.Insert(ctx, SubscriptionInsert{
			UserID:      u.ID,
			Provider:    "tg_stars",
			ProviderRef: ref,
			Plan:        "pro_30d",
			StartedAt:   now.Add(time.Duration(i) * time.Second),
			ExpiresAt:   &expires,
		})
		require.NoError(t, err)
	}

	subs, err := sr.ListByUser(ctx, u.ID)
	require.NoError(t, err)
	require.Len(t, subs, 3)
	require.Equal(t, "r3", subs[0].ProviderRef, "newest first")
	require.Equal(t, "r1", subs[2].ProviderRef)
}

// TestSubscriptionsRepo_LatestActive_HappyPath returns the row whose
// expires_at is in the future and ignores expired ones.
func TestSubscriptionsRepo_LatestActive_HappyPath(t *testing.T) {
	ctx := context.Background()
	p := setupTestPool(t)
	truncate(t, p, "subscriptions, users")

	ur := NewUsersRepo(p)
	sr := NewSubscriptionsRepo(p)
	u, _, err := ur.GetOrCreate(ctx, 102, "carol")
	require.NoError(t, err)

	now := time.Now().UTC().Truncate(time.Second)
	past := now.Add(-24 * time.Hour)
	future := now.Add(24 * time.Hour)

	_, _, err = sr.Insert(ctx, SubscriptionInsert{
		UserID: u.ID, Provider: "tg_stars", ProviderRef: "old",
		Plan: "pro_30d", StartedAt: now.Add(-48 * time.Hour), ExpiresAt: &past,
	})
	require.NoError(t, err)
	_, _, err = sr.Insert(ctx, SubscriptionInsert{
		UserID: u.ID, Provider: "tg_stars", ProviderRef: "new",
		Plan: "pro_30d", StartedAt: now, ExpiresAt: &future,
	})
	require.NoError(t, err)

	active, err := sr.LatestActive(ctx, u.ID)
	require.NoError(t, err)
	require.Equal(t, "new", active.ProviderRef)
}

// TestSubscriptionsRepo_LatestActive_NoneReturnsErrNoRows lets callers use
// errors.Is(err, pgx.ErrNoRows) to detect Free users.
func TestSubscriptionsRepo_LatestActive_NoneReturnsErrNoRows(t *testing.T) {
	ctx := context.Background()
	p := setupTestPool(t)
	truncate(t, p, "subscriptions, users")

	ur := NewUsersRepo(p)
	sr := NewSubscriptionsRepo(p)
	u, _, err := ur.GetOrCreate(ctx, 103, "dave")
	require.NoError(t, err)

	_, err = sr.LatestActive(ctx, u.ID)
	require.Error(t, err)
	require.True(t, errors.Is(err, pgx.ErrNoRows))
}
