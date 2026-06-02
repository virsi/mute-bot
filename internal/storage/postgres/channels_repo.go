// Package postgres provides pgxpool-backed repositories for each aggregate
// root (channels, posts, clusters, users, settings, subscriptions,
// deliveries, user_topics, session_state). Each repo owns one table; the
// Pool wrapper handles pgvector type registration.
package postgres

import (
	"context"
	"fmt"
)

// Channel is the read model returned by ChannelsRepo.
type Channel struct {
	ID          int64
	TGChannelID int64
	Username    string
	Title       string
	Authority   int
	Active      bool
}

// ChannelInsert carries the fields required by Upsert.
type ChannelInsert struct {
	TGChannelID int64
	Username    string
	Title       string
	Authority   int
}

// ChannelsRepo persists Telegram channels we read from.
type ChannelsRepo struct{ p *Pool }

// NewChannelsRepo constructs a ChannelsRepo bound to p.
func NewChannelsRepo(p *Pool) *ChannelsRepo { return &ChannelsRepo{p: p} }

// Upsert inserts a channel or updates its username/title/authority if a row
// with the same tg_channel_id already exists. Returns the channel id.
func (r *ChannelsRepo) Upsert(ctx context.Context, in ChannelInsert) (int64, error) {
	const q = `
		INSERT INTO channels (tg_channel_id, username, title, authority_score)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (tg_channel_id) DO UPDATE
		   SET username = EXCLUDED.username,
		       title = EXCLUDED.title,
		       authority_score = EXCLUDED.authority_score
		RETURNING id`
	var id int64
	if err := r.p.Pool().QueryRow(ctx, q, in.TGChannelID, in.Username, in.Title, in.Authority).Scan(&id); err != nil {
		return 0, fmt.Errorf("upsert channel: %w", err)
	}
	return id, nil
}

// ResolveOrCreate returns the internal id for tgID, inserting a placeholder
// channel row if one does not yet exist. Unlike Upsert it preserves
// previously-set username/title/authority — it is intended for hot paths
// (normalizer) that only know the tg-side channel id and must keep moving
// even if metadata has not been backfilled yet.
func (r *ChannelsRepo) ResolveOrCreate(ctx context.Context, tgID int64) (int64, error) {
	const q = `
		INSERT INTO channels (tg_channel_id)
		VALUES ($1)
		ON CONFLICT (tg_channel_id) DO UPDATE
		   SET tg_channel_id = EXCLUDED.tg_channel_id
		RETURNING id`
	var id int64
	if err := r.p.Pool().QueryRow(ctx, q, tgID).Scan(&id); err != nil {
		return 0, fmt.Errorf("resolve or create channel: %w", err)
	}
	return id, nil
}

// GetByTGID looks up a channel by its Telegram-side identifier.
func (r *ChannelsRepo) GetByTGID(ctx context.Context, tgID int64) (Channel, error) {
	const q = `
		SELECT id, tg_channel_id, COALESCE(username, ''), COALESCE(title, ''),
		       authority_score, active
		FROM channels
		WHERE tg_channel_id = $1`
	var c Channel
	if err := r.p.Pool().QueryRow(ctx, q, tgID).
		Scan(&c.ID, &c.TGChannelID, &c.Username, &c.Title, &c.Authority, &c.Active); err != nil {
		return Channel{}, fmt.Errorf("get channel: %w", err)
	}
	return c, nil
}

// SourcesForCluster returns the distinct, non-empty usernames of the
// channels that contributed posts to clusterID. The digest formatter renders
// these as "@username" tokens. Order is unspecified.
func (r *ChannelsRepo) SourcesForCluster(ctx context.Context, clusterID int64) ([]string, error) {
	const q = `
		SELECT DISTINCT COALESCE(ch.username, '')
		FROM posts p
		JOIN channels ch ON ch.id = p.channel_id
		WHERE p.cluster_id = $1
		  AND ch.username IS NOT NULL
		  AND ch.username <> ''`
	rows, err := r.p.Pool().Query(ctx, q, clusterID)
	if err != nil {
		return nil, fmt.Errorf("sources for cluster: %w", err)
	}
	defer rows.Close()
	out := make([]string, 0, 8)
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, fmt.Errorf("scan source: %w", err)
		}
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows err: %w", err)
	}
	return out, nil
}

// ListActive returns all channels with active = true, ordered by id.
func (r *ChannelsRepo) ListActive(ctx context.Context) ([]Channel, error) {
	const q = `
		SELECT id, tg_channel_id, COALESCE(username, ''), COALESCE(title, ''),
		       authority_score, active
		FROM channels
		WHERE active = true
		ORDER BY id`
	rows, err := r.p.Pool().Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("list active: %w", err)
	}
	defer rows.Close()
	out := make([]Channel, 0, 32)
	for rows.Next() {
		var c Channel
		if err := rows.Scan(&c.ID, &c.TGChannelID, &c.Username, &c.Title, &c.Authority, &c.Active); err != nil {
			return nil, fmt.Errorf("scan channel: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows err: %w", err)
	}
	return out, nil
}
