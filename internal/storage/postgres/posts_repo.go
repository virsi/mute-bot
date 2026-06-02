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

// GetClusterID returns the cluster id attached to postID, or 0 if the post
// is not yet clustered. Used by the dedup matcher to evaluate MinHash and
// embedding candidates.
func (r *PostsRepo) GetClusterID(ctx context.Context, postID int64) (int64, error) {
	const q = `SELECT cluster_id FROM posts WHERE id = $1`
	var cid *int64
	if err := r.p.Pool().QueryRow(ctx, q, postID).Scan(&cid); err != nil {
		return 0, fmt.Errorf("get cluster id: %w", err)
	}
	if cid == nil {
		return 0, nil
	}
	return *cid, nil
}

// GetText returns text_clean for postID. Used by the dedup borderline
// reconciler to assemble the LLMJudge prompt.
func (r *PostsRepo) GetText(ctx context.Context, postID int64) (string, error) {
	var t string
	if err := r.p.Pool().QueryRow(ctx,
		`SELECT text_clean FROM posts WHERE id = $1`, postID).Scan(&t); err != nil {
		return "", fmt.Errorf("get text: %w", err)
	}
	return t, nil
}

// AttachCluster links a post to a cluster.
func (r *PostsRepo) AttachCluster(ctx context.Context, postID, clusterID int64) error {
	if _, err := r.p.Pool().Exec(ctx,
		`UPDATE posts SET cluster_id = $2 WHERE id = $1`, postID, clusterID); err != nil {
		return fmt.Errorf("attach cluster: %w", err)
	}
	return nil
}

// ListTextsByCluster returns up to 10 cleaned post texts (oldest first) and
// the language of the first post attached to the cluster. Used by the
// classifier worker to assemble its prompt. The 10-post cap keeps prompt
// length under control for runaway clusters; the classifier itself further
// trims to a handful inside the prompt.
func (r *PostsRepo) ListTextsByCluster(ctx context.Context, clusterID int64) ([]string, string, error) {
	const q = `
		SELECT text_clean, COALESCE(lang, 'ru')
		FROM posts
		WHERE cluster_id = $1
		ORDER BY posted_at
		LIMIT 10`
	rows, err := r.p.Pool().Query(ctx, q, clusterID)
	if err != nil {
		return nil, "", fmt.Errorf("list texts by cluster: %w", err)
	}
	defer rows.Close()
	texts := make([]string, 0, 8)
	var lang string
	for rows.Next() {
		var t, l string
		if err := rows.Scan(&t, &l); err != nil {
			return nil, "", fmt.Errorf("scan post text: %w", err)
		}
		texts = append(texts, t)
		if lang == "" {
			lang = l
		}
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("rows err: %w", err)
	}
	return texts, lang, nil
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
