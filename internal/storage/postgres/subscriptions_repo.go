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
	ID          int64
	UserID      int64
	Provider    string
	ProviderRef string
	Plan        string
	StartedAt   time.Time
	ExpiresAt   *time.Time
}

// SubscriptionInsert is the create payload accepted by SubscriptionsRepo.
// ExpiresAt nil means lifetime (not used in M3 but supported by the schema).
type SubscriptionInsert struct {
	UserID      int64
	Provider    string
	ProviderRef string
	Plan        string
	StartedAt   time.Time
	ExpiresAt   *time.Time
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
		INSERT INTO subscriptions (user_id, provider, provider_ref, plan, started_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (provider, provider_ref) DO NOTHING
		RETURNING id`
	var id int64
	err := r.p.Pool().QueryRow(ctx, q,
		in.UserID, in.Provider, in.ProviderRef, in.Plan, in.StartedAt, in.ExpiresAt,
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
		SELECT id, user_id, provider, provider_ref, plan, started_at, expires_at
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
		SELECT id, user_id, provider, provider_ref, plan, started_at, expires_at
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
		SELECT id, user_id, provider, provider_ref, plan, started_at, expires_at
		  FROM subscriptions
		 WHERE provider = $1 AND provider_ref = $2`
	row := r.p.Pool().QueryRow(ctx, q, provider, ref)
	return scanSubscriptionRow(row)
}

// scanSubscription scans a pgx.Rows cursor into a Subscription.
func scanSubscription(rows pgx.Rows) (Subscription, error) {
	var s Subscription
	if err := rows.Scan(&s.ID, &s.UserID, &s.Provider, &s.ProviderRef, &s.Plan, &s.StartedAt, &s.ExpiresAt); err != nil {
		return Subscription{}, fmt.Errorf("scan subscription: %w", err)
	}
	return s, nil
}

// scanSubscriptionRow scans a pgx.Row into a Subscription. Returns
// pgx.ErrNoRows verbatim so callers can use errors.Is for the
// "not found" branch.
func scanSubscriptionRow(row pgx.Row) (Subscription, error) {
	var s Subscription
	if err := row.Scan(&s.ID, &s.UserID, &s.Provider, &s.ProviderRef, &s.Plan, &s.StartedAt, &s.ExpiresAt); err != nil {
		return Subscription{}, err
	}
	return s, nil
}
