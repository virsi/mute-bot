package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// SessionStateRepo persists the per-channel "last seen Telegram message id"
// used by the MTProto reader to resume after a restart without re-publishing
// posts that were already pushed onto the queue.
type SessionStateRepo struct{ p *Pool }

// NewSessionStateRepo binds the repo to a pool.
func NewSessionStateRepo(p *Pool) *SessionStateRepo { return &SessionStateRepo{p: p} }

// UpsertLastMsgID stores lastMsgID for channelID. If a row already exists
// the stored value is set to GREATEST(existing, new) so concurrent writers
// cannot move the cursor backwards.
func (r *SessionStateRepo) UpsertLastMsgID(ctx context.Context, channelID, lastMsgID int64) error {
	const q = `
		INSERT INTO session_state (channel_id, last_msg_id, last_updated_at)
		VALUES ($1, $2, now())
		ON CONFLICT (channel_id) DO UPDATE
		   SET last_msg_id     = GREATEST(session_state.last_msg_id, EXCLUDED.last_msg_id),
		       last_updated_at = now()`
	if _, err := r.p.Pool().Exec(ctx, q, channelID, lastMsgID); err != nil {
		return fmt.Errorf("upsert session state: %w", err)
	}
	return nil
}

// GetLastMsgID returns the stored cursor for channelID, or 0 if no row exists.
func (r *SessionStateRepo) GetLastMsgID(ctx context.Context, channelID int64) (int64, error) {
	const q = `SELECT last_msg_id FROM session_state WHERE channel_id = $1`
	var id int64
	err := r.p.Pool().QueryRow(ctx, q, channelID).Scan(&id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, nil
		}
		return 0, fmt.Errorf("get last msg id: %w", err)
	}
	return id, nil
}
