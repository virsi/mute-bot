package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/pgvector/pgvector-go"
)

// UserTopic is a Pro user's custom topic, stored alongside an embedding
// of the topic name. The embedding feeds the cluster-centroid match
// query in MatchesAny.
type UserTopic struct {
	ID        int64
	UserID    int64
	Name      string
	Embedding pgvector.Vector
	CreatedAt time.Time
}

// UserTopicsRepo persists Pro users' custom topics.
type UserTopicsRepo struct{ p *Pool }

// NewUserTopicsRepo constructs a UserTopicsRepo bound to p.
func NewUserTopicsRepo(p *Pool) *UserTopicsRepo { return &UserTopicsRepo{p: p} }

// Insert persists a new topic and returns its id. Returns a wrapped pgx
// unique-violation error when (user_id, name) collides — callers can
// surface a friendly "topic already exists" message on top.
func (r *UserTopicsRepo) Insert(ctx context.Context, userID int64, name string, emb pgvector.Vector) (int64, error) {
	const q = `
		INSERT INTO user_topics (user_id, name, embedding)
		VALUES ($1, $2, $3)
		RETURNING id`
	var id int64
	if err := r.p.Pool().QueryRow(ctx, q, userID, name, emb).Scan(&id); err != nil {
		return 0, fmt.Errorf("insert user topic: %w", err)
	}
	return id, nil
}

// ListByUser returns every custom topic for userID, oldest first. The
// embedding column is included so the assembler does not need a second
// round-trip when it wants to do the cosine match in process — for the
// hot path it should prefer MatchesAny, which does the math in SQL.
func (r *UserTopicsRepo) ListByUser(ctx context.Context, userID int64) ([]UserTopic, error) {
	const q = `
		SELECT id, user_id, name, embedding, created_at
		  FROM user_topics
		 WHERE user_id = $1
		 ORDER BY created_at ASC, id ASC`
	rows, err := r.p.Pool().Query(ctx, q, userID)
	if err != nil {
		return nil, fmt.Errorf("list user topics: %w", err)
	}
	defer rows.Close()
	out := make([]UserTopic, 0)
	for rows.Next() {
		var t UserTopic
		if err := rows.Scan(&t.ID, &t.UserID, &t.Name, &t.Embedding, &t.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan user topic: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows err: %w", err)
	}
	return out, nil
}

// Count returns how many topics userID owns. Drives the per-user cap
// check in topics.Service.AddTopic without paying for ListByUser's
// embedding bytes.
func (r *UserTopicsRepo) Count(ctx context.Context, userID int64) (int, error) {
	const q = `SELECT COUNT(*) FROM user_topics WHERE user_id = $1`
	var n int
	if err := r.p.Pool().QueryRow(ctx, q, userID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count user topics: %w", err)
	}
	return n, nil
}

// Delete removes the topic with the given name. Idempotent: removing a
// missing row is not an error so /topics remove never confuses the user
// with a "topic does not exist" race.
func (r *UserTopicsRepo) Delete(ctx context.Context, userID int64, name string) error {
	if _, err := r.p.Pool().Exec(ctx,
		`DELETE FROM user_topics WHERE user_id = $1 AND name = $2`,
		userID, name,
	); err != nil {
		return fmt.Errorf("delete user topic: %w", err)
	}
	return nil
}

// MatchesAny returns true when the cosine distance between v and at
// least one of userID's topic embeddings is <= maxDistance. The check
// is pushed into Postgres so the digest hot path costs a single round
// trip per (user, cluster) pair instead of N for the topic list.
//
// When userID has no custom topics, returns false — callers in the
// digest treat that as "no filter active" themselves (see Service.MatchesAny).
func (r *UserTopicsRepo) MatchesAny(ctx context.Context, userID int64, v pgvector.Vector, maxDistance float32) (bool, error) {
	const q = `
		SELECT EXISTS (
			SELECT 1 FROM user_topics
			 WHERE user_id = $1
			   AND (embedding <=> $2) <= $3
		)`
	var ok bool
	if err := r.p.Pool().QueryRow(ctx, q, userID, v, maxDistance).Scan(&ok); err != nil {
		return false, fmt.Errorf("matches any: %w", err)
	}
	return ok, nil
}
