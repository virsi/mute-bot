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

// TestSubscriptionsRepo_Insert_StoresPaymentMethodID locks in the M2
// requirement: YooKassa rows carry the saved payment_method_id so the
// renewer can charge the card later.
func TestSubscriptionsRepo_Insert_StoresPaymentMethodID(t *testing.T) {
	ctx := context.Background()
	p := setupTestPool(t)
	truncate(t, p, "subscriptions, users")
	ur := NewUsersRepo(p)
	sr := NewSubscriptionsRepo(p)
	u, _, err := ur.GetOrCreate(ctx, 9201, "yk-pm-user")
	require.NoError(t, err)
	started := time.Now().UTC().Truncate(time.Second)
	expires := started.Add(30 * 24 * time.Hour)

	_, isNew, err := sr.Insert(ctx, SubscriptionInsert{
		UserID: u.ID, Provider: "yookassa", ProviderRef: "pay-1",
		Plan: "pro_30d_rub", StartedAt: started, ExpiresAt: &expires,
		PaymentMethodID: "pm-1",
	})
	require.NoError(t, err)
	require.True(t, isNew)

	list, err := sr.ListByUser(ctx, u.ID)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, "pm-1", list[0].PaymentMethodID)
}

// TestSubscriptionsRepo_Insert_EmptyPaymentMethodID_NullStored confirms
// Stars rows (which have no saved card) land with NULL — the partial
// renew index then ignores them as required.
func TestSubscriptionsRepo_Insert_EmptyPaymentMethodID_NullStored(t *testing.T) {
	ctx := context.Background()
	p := setupTestPool(t)
	truncate(t, p, "subscriptions, users")
	ur := NewUsersRepo(p)
	sr := NewSubscriptionsRepo(p)
	u, _, err := ur.GetOrCreate(ctx, 9211, "stars-user")
	require.NoError(t, err)
	started := time.Now().UTC().Truncate(time.Second)
	expires := started.Add(30 * 24 * time.Hour)

	_, _, err = sr.Insert(ctx, SubscriptionInsert{
		UserID: u.ID, Provider: "tg_stars", ProviderRef: "stars-ref-1",
		Plan: "pro_30d", StartedAt: started, ExpiresAt: &expires,
		// PaymentMethodID left empty
	})
	require.NoError(t, err)

	var pm *string
	err = p.Pool().QueryRow(ctx, `SELECT payment_method_id FROM subscriptions WHERE provider='tg_stars' AND provider_ref='stars-ref-1'`).Scan(&pm)
	require.NoError(t, err)
	require.Nil(t, pm, "empty PaymentMethodID must map to SQL NULL")
}

// TestSubscriptionsRepo_ListExpiring_WindowFilter returns only the row
// whose expires_at falls inside the given window.
func TestSubscriptionsRepo_ListExpiring_WindowFilter(t *testing.T) {
	ctx := context.Background()
	p := setupTestPool(t)
	truncate(t, p, "subscriptions, users")
	ur := NewUsersRepo(p)
	sr := NewSubscriptionsRepo(p)
	u, _, err := ur.GetOrCreate(ctx, 9202, "yk-window")
	require.NoError(t, err)
	// Manually mark Pro so the JOIN survives.
	_, err = p.Pool().Exec(ctx, `UPDATE users SET tier='pro', tier_until=now()+interval '40 days' WHERE id=$1`, u.ID)
	require.NoError(t, err)
	soon := time.Now().Add(20 * time.Hour)
	far := time.Now().Add(40 * 24 * time.Hour)
	_, _, err = sr.Insert(ctx, SubscriptionInsert{
		UserID: u.ID, Provider: "yookassa", ProviderRef: "p1",
		Plan: "pro_30d_rub", StartedAt: time.Now(), ExpiresAt: &soon,
		PaymentMethodID: "pm-1",
	})
	require.NoError(t, err)
	_, _, err = sr.Insert(ctx, SubscriptionInsert{
		UserID: u.ID, Provider: "yookassa", ProviderRef: "p2",
		Plan: "pro_30d_rub", StartedAt: time.Now(), ExpiresAt: &far,
		PaymentMethodID: "pm-1",
	})
	require.NoError(t, err)

	out, err := sr.ListExpiring(ctx, 24*time.Hour)
	require.NoError(t, err)
	require.Len(t, out, 1, "DISTINCT ON keeps one row per user")
	require.Equal(t, "pm-1", out[0].PaymentMethodID)
	require.Equal(t, "yookassa", out[0].Provider)
	require.Equal(t, int64(9202), out[0].TGUserID)
}

// TestSubscriptionsRepo_ListExpiring_SkipsRowsWithoutPaymentMethod
// guarantees the renewer never tries to charge a Stars subscription —
// the column is NULL so the partial index excludes the row.
func TestSubscriptionsRepo_ListExpiring_SkipsRowsWithoutPaymentMethod(t *testing.T) {
	ctx := context.Background()
	p := setupTestPool(t)
	truncate(t, p, "subscriptions, users")
	ur := NewUsersRepo(p)
	sr := NewSubscriptionsRepo(p)
	u, _, err := ur.GetOrCreate(ctx, 9203, "stars-pro")
	require.NoError(t, err)
	_, err = p.Pool().Exec(ctx, `UPDATE users SET tier='pro', tier_until=now()+interval '40 days' WHERE id=$1`, u.ID)
	require.NoError(t, err)
	soon := time.Now().Add(20 * time.Hour)
	_, _, err = sr.Insert(ctx, SubscriptionInsert{
		UserID: u.ID, Provider: "tg_stars", ProviderRef: "s1",
		Plan: "pro_30d", StartedAt: time.Now(), ExpiresAt: &soon,
		// No payment_method_id — Stars rows leave it NULL.
	})
	require.NoError(t, err)

	out, err := sr.ListExpiring(ctx, 24*time.Hour)
	require.NoError(t, err)
	require.Empty(t, out)
}

// TestSubscriptionsRepo_ListExpiring_SkipsFreeUsers locks in the Pro-gate
// in the SQL: a Pro subscription row owned by a downgraded user must not
// be auto-renewed.
func TestSubscriptionsRepo_ListExpiring_SkipsFreeUsers(t *testing.T) {
	ctx := context.Background()
	p := setupTestPool(t)
	truncate(t, p, "subscriptions, users")
	ur := NewUsersRepo(p)
	sr := NewSubscriptionsRepo(p)
	u, _, err := ur.GetOrCreate(ctx, 9204, "downgrade")
	require.NoError(t, err)
	// User stays on free tier — the JOIN must filter them out.
	soon := time.Now().Add(20 * time.Hour)
	_, _, err = sr.Insert(ctx, SubscriptionInsert{
		UserID: u.ID, Provider: "yookassa", ProviderRef: "p1",
		Plan: "pro_30d_rub", StartedAt: time.Now(), ExpiresAt: &soon,
		PaymentMethodID: "pm-1",
	})
	require.NoError(t, err)
	out, err := sr.ListExpiring(ctx, 24*time.Hour)
	require.NoError(t, err)
	require.Empty(t, out)
}
