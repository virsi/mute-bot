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

// SetBlocked flips the blocked flag (used when Telegram returns
// "bot was blocked by the user" on send).
func (r *UsersRepo) SetBlocked(ctx context.Context, id int64, blocked bool) error {
	if _, err := r.p.Pool().Exec(ctx, `UPDATE users SET blocked = $2 WHERE id = $1`, id, blocked); err != nil {
		return fmt.Errorf("set blocked: %w", err)
	}
	return nil
}
