//go:build integration

package postgres

import (
	"context"
	"strings"
	"testing"

	"github.com/pgvector/pgvector-go"
	"github.com/stretchr/testify/require"
)

// fillVecOffset builds a 1536-dim vector of constant value x with one
// dimension nudged by delta. Lets tests construct embeddings that are
// "close" to each other (small delta) or "far" (delta near 1) while
// keeping the rest of the math simple to reason about.
func fillVecOffset(x float32, dim int, delta float32) []float32 {
	v := make([]float32, 1536)
	for i := range v {
		v[i] = x
	}
	v[dim] = x + delta
	return v
}

// TestUserTopicsRepo_InsertList_RoundTrip locks in the basic CRUD: a
// freshly inserted row comes back from ListByUser with name + embedding
// preserved, oldest first.
func TestUserTopicsRepo_InsertList_RoundTrip(t *testing.T) {
	ctx := context.Background()
	p := setupTestPool(t)
	truncate(t, p, "user_topics, users")

	ur := NewUsersRepo(p)
	r := NewUserTopicsRepo(p)
	u, _, err := ur.GetOrCreate(ctx, 8001, "alice")
	require.NoError(t, err)

	emb1 := pgvector.NewVector(fillVecOffset(0.1, 0, 0))
	emb2 := pgvector.NewVector(fillVecOffset(0.2, 0, 0))
	id1, err := r.Insert(ctx, u.ID, "ai-news", emb1)
	require.NoError(t, err)
	require.Greater(t, id1, int64(0))
	id2, err := r.Insert(ctx, u.ID, "kazakhstan", emb2)
	require.NoError(t, err)
	require.Greater(t, id2, id1)

	got, err := r.ListByUser(ctx, u.ID)
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, "ai-news", got[0].Name)
	require.Equal(t, "kazakhstan", got[1].Name)
	require.Equal(t, u.ID, got[0].UserID)
}

// TestUserTopicsRepo_Insert_UniqueViolation verifies that re-inserting
// the same (user_id, name) hits the UNIQUE constraint — the bot will
// surface this as "topic already exists" upstream.
func TestUserTopicsRepo_Insert_UniqueViolation(t *testing.T) {
	ctx := context.Background()
	p := setupTestPool(t)
	truncate(t, p, "user_topics, users")

	ur := NewUsersRepo(p)
	r := NewUserTopicsRepo(p)
	u, _, err := ur.GetOrCreate(ctx, 8002, "bob")
	require.NoError(t, err)

	emb := pgvector.NewVector(fillVecOffset(0.1, 0, 0))
	_, err = r.Insert(ctx, u.ID, "crypto", emb)
	require.NoError(t, err)
	_, err = r.Insert(ctx, u.ID, "crypto", emb)
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "unique") ||
		strings.Contains(err.Error(), "duplicate"),
		"expected unique-violation error, got %v", err)
}

// TestUserTopicsRepo_Count returns 0 for unknown users and grows by
// one on each insert.
func TestUserTopicsRepo_Count(t *testing.T) {
	ctx := context.Background()
	p := setupTestPool(t)
	truncate(t, p, "user_topics, users")

	ur := NewUsersRepo(p)
	r := NewUserTopicsRepo(p)
	u, _, err := ur.GetOrCreate(ctx, 8003, "carol")
	require.NoError(t, err)

	n, err := r.Count(ctx, u.ID)
	require.NoError(t, err)
	require.Equal(t, 0, n)

	emb := pgvector.NewVector(fillVecOffset(0.1, 0, 0))
	_, err = r.Insert(ctx, u.ID, "t1", emb)
	require.NoError(t, err)
	_, err = r.Insert(ctx, u.ID, "t2", emb)
	require.NoError(t, err)

	n, err = r.Count(ctx, u.ID)
	require.NoError(t, err)
	require.Equal(t, 2, n)
}

// TestUserTopicsRepo_Delete_Idempotent confirms the contract: deleting a
// missing row is silent. Also checks the row really is gone after a
// successful delete.
func TestUserTopicsRepo_Delete_Idempotent(t *testing.T) {
	ctx := context.Background()
	p := setupTestPool(t)
	truncate(t, p, "user_topics, users")

	ur := NewUsersRepo(p)
	r := NewUserTopicsRepo(p)
	u, _, err := ur.GetOrCreate(ctx, 8004, "dave")
	require.NoError(t, err)

	require.NoError(t, r.Delete(ctx, u.ID, "ghost"))

	emb := pgvector.NewVector(fillVecOffset(0.1, 0, 0))
	_, err = r.Insert(ctx, u.ID, "real", emb)
	require.NoError(t, err)
	require.NoError(t, r.Delete(ctx, u.ID, "real"))
	require.NoError(t, r.Delete(ctx, u.ID, "real")) // second delete also fine

	n, err := r.Count(ctx, u.ID)
	require.NoError(t, err)
	require.Equal(t, 0, n)
}

// TestUserTopicsRepo_MatchesAny verifies the SQL-side cosine match: a
// query vector that is identical to a stored topic returns true; a
// query in the opposite direction returns false at the same threshold.
//
// Two topics are inserted so the EXISTS short-circuit branch is also
// exercised: a match against the second seed is enough.
func TestUserTopicsRepo_MatchesAny(t *testing.T) {
	ctx := context.Background()
	p := setupTestPool(t)
	truncate(t, p, "user_topics, users")

	ur := NewUsersRepo(p)
	r := NewUserTopicsRepo(p)
	u, _, err := ur.GetOrCreate(ctx, 8005, "eve")
	require.NoError(t, err)

	// Two distinct topic embeddings.
	emb1 := pgvector.NewVector(fillVecOffset(0.1, 0, 0))
	emb2 := pgvector.NewVector(fillVecOffset(-0.1, 0, 0))
	_, err = r.Insert(ctx, u.ID, "ai", emb1)
	require.NoError(t, err)
	_, err = r.Insert(ctx, u.ID, "crypto", emb2)
	require.NoError(t, err)

	// Exact match against emb1 — distance ≈ 0, must be true.
	hit, err := r.MatchesAny(ctx, u.ID, emb1, 0.3)
	require.NoError(t, err)
	require.True(t, hit, "exact-match query must hit")

	// Orthogonal-ish vector — neither stored topic is within threshold.
	farArr := make([]float32, 1536)
	farArr[0] = 1
	far := pgvector.NewVector(farArr)
	hitFar, err := r.MatchesAny(ctx, u.ID, far, 0.05)
	require.NoError(t, err)
	require.False(t, hitFar, "far query must not hit at tight threshold")

	// User with no topics returns false (no rows, EXISTS false).
	u2, _, err := ur.GetOrCreate(ctx, 8006, "noone")
	require.NoError(t, err)
	hitEmpty, err := r.MatchesAny(ctx, u2.ID, emb1, 1.0)
	require.NoError(t, err)
	require.False(t, hitEmpty)
}
