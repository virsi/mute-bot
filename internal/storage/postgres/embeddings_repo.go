package postgres

import (
	"context"
	"fmt"

	"github.com/pgvector/pgvector-go"
)

// EmbeddingsRepo stores per-post embeddings and runs kNN queries against
// pgvector to find similar recent posts (dedup candidate generation).
type EmbeddingsRepo struct{ p *Pool }

// NewEmbeddingsRepo constructs an EmbeddingsRepo bound to p.
func NewEmbeddingsRepo(p *Pool) *EmbeddingsRepo { return &EmbeddingsRepo{p: p} }

// Neighbor is a single nearest-neighbor result with cosine distance.
type Neighbor struct {
	PostID   int64
	Distance float32
}

// NearestParams parameterises NearestNeighbors.
type NearestParams struct {
	Limit             int
	MaxCosineDistance float32
	// SinceWindowSecs limits the search to posts ingested within the last N
	// seconds. Zero defaults to 48h — matches the dedup window.
	SinceWindowSecs int
}

// Store upserts the embedding for postID.
func (r *EmbeddingsRepo) Store(ctx context.Context, postID int64, v pgvector.Vector, model string) error {
	const q = `
		INSERT INTO post_embeddings (post_id, embedding, model)
		VALUES ($1, $2, $3)
		ON CONFLICT (post_id) DO UPDATE
		   SET embedding = EXCLUDED.embedding,
		       model = EXCLUDED.model`
	if _, err := r.p.Pool().Exec(ctx, q, postID, v, model); err != nil {
		return fmt.Errorf("store embedding: %w", err)
	}
	return nil
}

// NearestNeighbors returns posts with cosine distance to v at most
// MaxCosineDistance, ordered by ascending distance and capped by Limit.
//
// The query joins to posts so we can restrict by ingested_at — clusters
// only need to consider posts in the active window (typically 48h).
func (r *EmbeddingsRepo) NearestNeighbors(ctx context.Context, v pgvector.Vector, p NearestParams) ([]Neighbor, error) {
	if p.SinceWindowSecs == 0 {
		p.SinceWindowSecs = 48 * 3600
	}
	const q = `
		SELECT e.post_id, (e.embedding <=> $1) AS dist
		FROM post_embeddings e
		JOIN posts po ON po.id = e.post_id
		WHERE po.ingested_at >= now() - make_interval(secs => $2)
		  AND (e.embedding <=> $1) <= $3
		ORDER BY e.embedding <=> $1
		LIMIT $4`
	rows, err := r.p.Pool().Query(ctx, q, v, p.SinceWindowSecs, p.MaxCosineDistance, p.Limit)
	if err != nil {
		return nil, fmt.Errorf("nn query: %w", err)
	}
	defer rows.Close()
	out := make([]Neighbor, 0, p.Limit)
	for rows.Next() {
		var n Neighbor
		if err := rows.Scan(&n.PostID, &n.Distance); err != nil {
			return nil, fmt.Errorf("scan neighbor: %w", err)
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows err: %w", err)
	}
	return out, nil
}
