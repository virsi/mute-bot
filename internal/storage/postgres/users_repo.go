package postgres

import (
	"context"
	"fmt"
	"time"
)

// User is the persistence-side user row.
type User struct {
	ID        int64
	TGUserID  int64
	Username  string
	Tier      string
	TierUntil *time.Time
	TrialUsed bool
	Lang      string
	Blocked   bool
	CreatedAt time.Time
}

// UsersRepo persists Telegram users known to the bot.
type UsersRepo struct{ p *Pool }

// NewUsersRepo constructs a UsersRepo bound to p.
func NewUsersRepo(p *Pool) *UsersRepo { return &UsersRepo{p: p} }

// GetOrCreate ensures a row exists for tgID. Returns the row and a bool
// indicating whether the row was newly created on this call. If the row
// already existed, tg_username is refreshed.
//
// The (xmax = 0) trick on the returning clause distinguishes INSERT from
// UPDATE-via-conflict: xmax is zero only for freshly inserted tuples.
func (r *UsersRepo) GetOrCreate(ctx context.Context, tgID int64, username string) (User, bool, error) {
	const q = `
		INSERT INTO users (tg_user_id, tg_username)
		VALUES ($1, $2)
		ON CONFLICT (tg_user_id) DO UPDATE SET tg_username = EXCLUDED.tg_username
		RETURNING id, tg_user_id, COALESCE(tg_username, ''), tier, tier_until,
		          trial_used, lang, blocked, created_at,
		          (xmax = 0) AS created`
	var u User
	var created bool
	if err := r.p.Pool().QueryRow(ctx, q, tgID, username).Scan(
		&u.ID, &u.TGUserID, &u.Username, &u.Tier, &u.TierUntil,
		&u.TrialUsed, &u.Lang, &u.Blocked, &u.CreatedAt, &created,
	); err != nil {
		return User{}, false, fmt.Errorf("get_or_create user: %w", err)
	}
	return u, created, nil
}

// GetByID returns the user row by internal id. Used by the digest
// assembler to find out the tier of the recipient before applying the
// Pro-only custom-topic filter; pgx.ErrNoRows is surfaced verbatim so
// callers can branch on errors.Is.
func (r *UsersRepo) GetByID(ctx context.Context, id int64) (User, error) {
	const q = `
		SELECT id, tg_user_id, COALESCE(tg_username, ''), tier, tier_until,
		       trial_used, lang, blocked, created_at
		  FROM users
		 WHERE id = $1`
	var u User
	if err := r.p.Pool().QueryRow(ctx, q, id).Scan(
		&u.ID, &u.TGUserID, &u.Username, &u.Tier, &u.TierUntil,
		&u.TrialUsed, &u.Lang, &u.Blocked, &u.CreatedAt,
	); err != nil {
		return User{}, fmt.Errorf("get user by id: %w", err)
	}
	return u, nil
}

// SetBlocked flips the blocked flag (used when Telegram returns
// "bot was blocked by the user" on send).
func (r *UsersRepo) SetBlocked(ctx context.Context, id int64, blocked bool) error {
	if _, err := r.p.Pool().Exec(ctx, `UPDATE users SET blocked = $2 WHERE id = $1`, id, blocked); err != nil {
		return fmt.Errorf("set blocked: %w", err)
	}
	return nil
}

// GrantPro upgrades id to tier='pro' and extends tier_until by dur. When the
// user is already pro with a tier_until in the future, the new deadline is
// added on top of the existing one — calling GrantPro twice with 30 days
// yields 60 days of Pro time. When the user is free (or already-expired pro),
// the extension starts from now().
//
// The GREATEST/COALESCE dance keeps the math simple in a single UPDATE and
// avoids a read-then-write race when two webhooks arrive close in time.
func (r *UsersRepo) GrantPro(ctx context.Context, id int64, dur time.Duration) error {
	const q = `
		UPDATE users
		   SET tier       = 'pro',
		       tier_until = GREATEST(COALESCE(tier_until, now()), now()) + $2::interval
		 WHERE id = $1`
	iv := fmt.Sprintf("%d seconds", int(dur.Seconds()))
	if _, err := r.p.Pool().Exec(ctx, q, id, iv); err != nil {
		return fmt.Errorf("grant pro: %w", err)
	}
	return nil
}

// ListExpired returns ids of pro users whose tier_until is non-null and
// no later than asOf. Drives the hourly expiry sweeper that downgrades
// expired Pro users back to free.
func (r *UsersRepo) ListExpired(ctx context.Context, asOf time.Time) ([]int64, error) {
	const q = `
		SELECT id
		  FROM users
		 WHERE tier = 'pro'
		   AND tier_until IS NOT NULL
		   AND tier_until <= $1`
	rows, err := r.p.Pool().Query(ctx, q, asOf)
	if err != nil {
		return nil, fmt.Errorf("list expired: %w", err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan id: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows err: %w", err)
	}
	return out, nil
}

// SetTier writes tier and tier_until verbatim. Passing until=nil clears
// the deadline (used by the expiry sweeper when downgrading to free).
func (r *UsersRepo) SetTier(ctx context.Context, id int64, tier string, until *time.Time) error {
	if _, err := r.p.Pool().Exec(ctx,
		`UPDATE users SET tier = $2, tier_until = $3 WHERE id = $1`,
		id, tier, until,
	); err != nil {
		return fmt.Errorf("set tier: %w", err)
	}
	return nil
}

// AlertEligible is the read model joining users + user_settings into one
// row per Pro user with alerts_enabled = true. The alerts worker uses it
// to decide who receives a real-time push without N+1 lookups.
type AlertEligible struct {
	UserID      int64
	TGUserID    int64
	Threshold   int
	ThrottleMin int
}

// ListAlertEligible returns every non-blocked Pro user whose tier_until
// has not passed (NULL = lifetime) and whose alerts_enabled flag is set.
// The single JOIN keeps the lookup cheap even with a few thousand Pro
// users — adding an index on (tier, alerts_enabled) would be a follow-up
// optimisation if needed.
func (r *UsersRepo) ListAlertEligible(ctx context.Context) ([]AlertEligible, error) {
	const q = `
		SELECT u.id, u.tg_user_id, s.alert_threshold, s.alert_throttle_minutes
		  FROM users u
		  JOIN user_settings s ON s.user_id = u.id
		 WHERE u.blocked = false
		   AND u.tier = 'pro'
		   AND (u.tier_until IS NULL OR u.tier_until > now())
		   AND s.alerts_enabled = true`
	rows, err := r.p.Pool().Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("list alert eligible: %w", err)
	}
	defer rows.Close()
	var out []AlertEligible
	for rows.Next() {
		var e AlertEligible
		if err := rows.Scan(&e.UserID, &e.TGUserID, &e.Threshold, &e.ThrottleMin); err != nil {
			return nil, fmt.Errorf("scan eligible: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows err: %w", err)
	}
	return out, nil
}

// BulkDowngradeExpired atomically downgrades every Pro user whose
// tier_until has passed back to free in a single UPDATE. Returns the ids
// that were actually flipped. Used by the hourly sweeper instead of the
// list-then-loop pattern so a concurrent GrantPro between the SELECT and
// UPDATE cannot lose data — the WHERE clause re-evaluates per row.
func (r *UsersRepo) BulkDowngradeExpired(ctx context.Context, asOf time.Time) ([]int64, error) {
	const q = `
		UPDATE users
		   SET tier = 'free',
		       tier_until = NULL
		 WHERE tier = 'pro'
		   AND tier_until IS NOT NULL
		   AND tier_until <= $1
		RETURNING id`
	rows, err := r.p.Pool().Query(ctx, q, asOf)
	if err != nil {
		return nil, fmt.Errorf("bulk downgrade: %w", err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan id: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows err: %w", err)
	}
	return out, nil
}
