package postgres

import (
	"context"
	"fmt"
	"time"
)

// PostInsert carries the fields required by Insert.
type PostInsert struct {
	ChannelID int64
	TGMsgID   int64
	TextRaw   string
	TextClean string
	TextHash  []byte
	Lang      string
	PostedAt  time.Time
}

// Post is the read model returned by PostsRepo.
type Post struct {
	ID         int64
	ChannelID  int64
	TGMsgID    int64
	TextRaw    string
	TextClean  string
	TextHash   []byte
	Lang       string
	PostedAt   time.Time
	IngestedAt time.Time
	ClusterID  *int64
}

// PostsRepo persists normalized posts.
type PostsRepo struct{ p *Pool }

// NewPostsRepo constructs a PostsRepo bound to p.
func NewPostsRepo(p *Pool) *PostsRepo { return &PostsRepo{p: p} }

// Insert stores a post. If a row with the same (channel_id, tg_msg_id)
// already exists, the existing id is returned (idempotent against retries
// from the queue).
func (r *PostsRepo) Insert(ctx context.Context, in PostInsert) (int64, error) {
	const q = `
		INSERT INTO posts (channel_id, tg_msg_id, text_raw, text_clean, text_hash, lang, posted_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (channel_id, tg_msg_id) DO UPDATE
		   SET tg_msg_id = EXCLUDED.tg_msg_id
		RETURNING id`
	var id int64
	if err := r.p.Pool().QueryRow(ctx, q,
		in.ChannelID, in.TGMsgID, in.TextRaw, in.TextClean, in.TextHash, in.Lang, in.PostedAt,
	).Scan(&id); err != nil {
		return 0, fmt.Errorf("insert post: %w", err)
	}
	return id, nil
}

// AttachCluster links a post to a cluster.
func (r *PostsRepo) AttachCluster(ctx context.Context, postID, clusterID int64) error {
	if _, err := r.p.Pool().Exec(ctx,
		`UPDATE posts SET cluster_id = $2 WHERE id = $1`, postID, clusterID); err != nil {
		return fmt.Errorf("attach cluster: %w", err)
	}
	return nil
}

// ListByCluster returns all posts attached to clusterID, oldest first.
func (r *PostsRepo) ListByCluster(ctx context.Context, clusterID int64) ([]Post, error) {
	const q = `
		SELECT id, channel_id, tg_msg_id, text_raw, text_clean, text_hash,
		       COALESCE(lang, ''), posted_at, ingested_at, cluster_id
		FROM posts
		WHERE cluster_id = $1
		ORDER BY posted_at`
	rows, err := r.p.Pool().Query(ctx, q, clusterID)
	if err != nil {
		return nil, fmt.Errorf("list by cluster: %w", err)
	}
	defer rows.Close()
	out := make([]Post, 0, 8)
	for rows.Next() {
		var p Post
		if err := rows.Scan(&p.ID, &p.ChannelID, &p.TGMsgID, &p.TextRaw, &p.TextClean, &p.TextHash,
			&p.Lang, &p.PostedAt, &p.IngestedAt, &p.ClusterID); err != nil {
			return nil, fmt.Errorf("scan post: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows err: %w", err)
	}
	return out, nil
}
