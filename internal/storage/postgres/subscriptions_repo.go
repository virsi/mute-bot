package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Subscription is a persisted billing row.
type Subscription struct {
	ID              int64
	UserID          int64
	Provider        string
	ProviderRef     string
	Plan            string
	StartedAt       time.Time
	ExpiresAt       *time.Time
	PaymentMethodID string
}

// SubscriptionInsert is the create payload accepted by SubscriptionsRepo.
// ExpiresAt nil means lifetime (not used in M3 but supported by the schema).
// PaymentMethodID is set by providers that support saved-card autopayment
// (YooKassa). Stars rows leave it empty — Insert converts empty strings
// to SQL NULL so the partial index in migration 0007 stays accurate.
type SubscriptionInsert struct {
	UserID          int64
	Provider        string
	ProviderRef     string
	Plan            string
	StartedAt       time.Time
	ExpiresAt       *time.Time
	PaymentMethodID string
}

// ExpiringSubscription is the narrow read model the YooKassa renewer
// consumes: one row per Pro subscription that has a saved payment method
// and expires inside the renewal window. Provider keys the dispatch back
// to the right Provider instance in billing.Service.
type ExpiringSubscription struct {
	ID              int64
	UserID          int64
	TGUserID        int64
	Provider        string
	PaymentMethodID string
	ExpiresAt       time.Time
}

// SubscriptionsRepo persists billing activations and exposes lookups the
// billing service and bot-api need.
type SubscriptionsRepo struct{ p *Pool }

// NewSubscriptionsRepo constructs a SubscriptionsRepo bound to p.
func NewSubscriptionsRepo(p *Pool) *SubscriptionsRepo { return &SubscriptionsRepo{p: p} }

// Insert persists in and reports whether the row was actually inserted
// (isNew=false on UNIQUE(provider, provider_ref) conflict). Idempotent
// against retried webhooks — the caller can use isNew to decide whether to
// run side effects (e.g. GrantPro) exactly once.
//
// On conflict the existing row's id is fetched so callers can still refer
// to the persisted subscription.
func (r *SubscriptionsRepo) Insert(ctx context.Context, in SubscriptionInsert) (int64, bool, error) {
	const q = `
		INSERT INTO subscriptions (user_id, provider, provider_ref, plan, started_at, expires_at, payment_method_id)
		VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7, ''))
		ON CONFLICT (provider, provider_ref) DO NOTHING
		RETURNING id`
	var id int64
	err := r.p.Pool().QueryRow(ctx, q,
		in.UserID, in.Provider, in.ProviderRef, in.Plan, in.StartedAt, in.ExpiresAt, in.PaymentMethodID,
	).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		existing, gerr := r.getByRef(ctx, in.Provider, in.ProviderRef)
		if gerr != nil {
			return 0, false, fmt.Errorf("conflict lookup: %w", gerr)
		}
		return existing.ID, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("insert subscription: %w", err)
	}
	return id, true, nil
}

// ListByUser returns every subscription row for userID ordered by started_at
// desc. Used by /settings and admin tooling.
func (r *SubscriptionsRepo) ListByUser(ctx context.Context, userID int64) ([]Subscription, error) {
	const q = `
		SELECT id, user_id, provider, provider_ref, plan, started_at, expires_at, payment_method_id
		  FROM subscriptions
		 WHERE user_id = $1
		 ORDER BY started_at DESC`
	rows, err := r.p.Pool().Query(ctx, q, userID)
	if err != nil {
		return nil, fmt.Errorf("list by user: %w", err)
	}
	defer rows.Close()
	out := make([]Subscription, 0)
	for rows.Next() {
		s, scanErr := scanSubscription(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows err: %w", err)
	}
	return out, nil
}

// LatestActive returns the most-recently-started subscription whose
// expires_at is null (lifetime) or strictly in the future as of now().
// Returns (Subscription{}, pgx.ErrNoRows) when the user has no active row
// so callers can detect "Free user" via errors.Is.
func (r *SubscriptionsRepo) LatestActive(ctx context.Context, userID int64) (Subscription, error) {
	const q = `
		SELECT id, user_id, provider, provider_ref, plan, started_at, expires_at, payment_method_id
		  FROM subscriptions
		 WHERE user_id = $1
		   AND (expires_at IS NULL OR expires_at > now())
		 ORDER BY started_at DESC
		 LIMIT 1`
	row := r.p.Pool().QueryRow(ctx, q, userID)
	return scanSubscriptionRow(row)
}

// getByRef is the conflict-resolution path: when Insert hits ON CONFLICT,
// we still need the existing id to return.
func (r *SubscriptionsRepo) getByRef(ctx context.Context, provider, ref string) (Subscription, error) {
	const q = `
		SELECT id, user_id, provider, provider_ref, plan, started_at, expires_at, payment_method_id
		  FROM subscriptions
		 WHERE provider = $1 AND provider_ref = $2`
	row := r.p.Pool().QueryRow(ctx, q, provider, ref)
	return scanSubscriptionRow(row)
}

// ListExpiring returns active subscriptions whose expires_at falls in
// (now, now + window], have a non-NULL payment_method_id, and whose
// owning user is on the Pro tier and is not blocked. Newer rows shadow
// older ones via DISTINCT ON so only the latest payment_method_id per
// user is returned — protecting against double-charging when the user
// has more than one saved card on file.
func (r *SubscriptionsRepo) ListExpiring(ctx context.Context, window time.Duration) ([]ExpiringSubscription, error) {
	const q = `
		SELECT DISTINCT ON (s.user_id) s.id, s.user_id, u.tg_user_id,
		       s.provider, s.payment_method_id, s.expires_at
		  FROM subscriptions s
		  JOIN users u ON u.id = s.user_id
		 WHERE s.payment_method_id IS NOT NULL
		   AND s.expires_at IS NOT NULL
		   AND s.expires_at >  now()
		   AND s.expires_at <= now() + ($1::bigint || ' seconds')::interval
		   AND u.tier = 'pro'
		   AND u.blocked = false
		 ORDER BY s.user_id, s.expires_at DESC`
	rows, err := r.p.Pool().Query(ctx, q, int64(window.Seconds()))
	if err != nil {
		return nil, fmt.Errorf("list expiring: %w", err)
	}
	defer rows.Close()
	out := make([]ExpiringSubscription, 0)
	for rows.Next() {
		var e ExpiringSubscription
		if err := rows.Scan(&e.ID, &e.UserID, &e.TGUserID, &e.Provider, &e.PaymentMethodID, &e.ExpiresAt); err != nil {
			return nil, fmt.Errorf("scan expiring: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows err: %w", err)
	}
	return out, nil
}

// scanSubscription scans a pgx.Rows cursor into a Subscription.
func scanSubscription(rows pgx.Rows) (Subscription, error) {
	var s Subscription
	var pm *string
	if err := rows.Scan(&s.ID, &s.UserID, &s.Provider, &s.ProviderRef, &s.Plan, &s.StartedAt, &s.ExpiresAt, &pm); err != nil {
		return Subscription{}, fmt.Errorf("scan subscription: %w", err)
	}
	if pm != nil {
		s.PaymentMethodID = *pm
	}
	return s, nil
}

// scanSubscriptionRow scans a pgx.Row into a Subscription. Returns
// pgx.ErrNoRows verbatim so callers can use errors.Is for the
// "not found" branch.
func scanSubscriptionRow(row pgx.Row) (Subscription, error) {
	var s Subscription
	var pm *string
	if err := row.Scan(&s.ID, &s.UserID, &s.Provider, &s.ProviderRef, &s.Plan, &s.StartedAt, &s.ExpiresAt, &pm); err != nil {
		return Subscription{}, err
	}
	if pm != nil {
		s.PaymentMethodID = *pm
	}
	return s, nil
}
