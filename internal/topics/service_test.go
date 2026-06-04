package topics

import (
	"context"
	"errors"
	"testing"

	"github.com/pgvector/pgvector-go"
	"github.com/stretchr/testify/require"

	"github.com/virsi/mute-bot/internal/llm"
	"github.com/virsi/mute-bot/internal/storage/postgres"
)

// fakeRepo is an in-memory stand-in for postgres.UserTopicsRepo. Mirrors
// the small interface defined in ports.go and tracks every call so the
// tests can verify the exact MatchesAny path the assembler will hit.
type fakeRepo struct {
	rows []postgres.UserTopic
	// matchesAny is the canned response from MatchesAny; lets tests
	// flip "yes/no" without seeding meaningful vectors.
	matchesAnyResult bool
	matchesAnyErr    error
	matchesAnyCalls  int
	insertErr        error
	countErr         error
	listErr          error
	deleteErr        error
	deletedNames     []string
}

func (f *fakeRepo) Insert(_ context.Context, userID int64, name string, emb pgvector.Vector) (int64, error) {
	if f.insertErr != nil {
		return 0, f.insertErr
	}
	id := int64(len(f.rows) + 1)
	f.rows = append(f.rows, postgres.UserTopic{
		ID: id, UserID: userID, Name: name, Embedding: emb,
	})
	return id, nil
}

func (f *fakeRepo) ListByUser(_ context.Context, userID int64) ([]postgres.UserTopic, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]postgres.UserTopic, 0, len(f.rows))
	for _, r := range f.rows {
		if r.UserID == userID {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *fakeRepo) Count(_ context.Context, userID int64) (int, error) {
	if f.countErr != nil {
		return 0, f.countErr
	}
	n := 0
	for _, r := range f.rows {
		if r.UserID == userID {
			n++
		}
	}
	return n, nil
}

func (f *fakeRepo) Delete(_ context.Context, userID int64, name string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.deletedNames = append(f.deletedNames, name)
	kept := f.rows[:0]
	for _, r := range f.rows {
		if r.UserID == userID && r.Name == name {
			continue
		}
		kept = append(kept, r)
	}
	f.rows = kept
	return nil
}

func (f *fakeRepo) MatchesAny(_ context.Context, _ int64, _ pgvector.Vector, _ float32) (bool, error) {
	f.matchesAnyCalls++
	return f.matchesAnyResult, f.matchesAnyErr
}

// fakeEmb returns a fixed deterministic vector for every call so tests
// can read back the persisted embedding without doing any real math.
type fakeEmb struct {
	vec   []float32
	err   error
	calls int
}

func (e *fakeEmb) Embed(_ context.Context, _ llm.EmbedRequest) (llm.EmbedResponse, error) {
	e.calls++
	if e.err != nil {
		return llm.EmbedResponse{}, e.err
	}
	return llm.EmbedResponse{Vectors: [][]float32{e.vec}}, nil
}

func nonZeroVec() []float32 {
	v := make([]float32, 4)
	v[0] = 0.5
	return v
}

func TestAddTopic_PersistsEmbeddedName(t *testing.T) {
	repo := &fakeRepo{}
	emb := &fakeEmb{vec: nonZeroVec()}
	svc := NewService(Deps{Repo: repo, Embedder: emb})

	require.NoError(t, svc.AddTopic(context.Background(), 42, "ai-news"))
	require.Len(t, repo.rows, 1)
	require.Equal(t, "ai-news", repo.rows[0].Name)
	require.Equal(t, int64(42), repo.rows[0].UserID)
	require.Equal(t, 1, emb.calls, "embedder must be called exactly once on AddTopic")
}

func TestAddTopic_TrimWhitespace(t *testing.T) {
	repo := &fakeRepo{}
	emb := &fakeEmb{vec: nonZeroVec()}
	svc := NewService(Deps{Repo: repo, Embedder: emb})

	require.NoError(t, svc.AddTopic(context.Background(), 1, "  crypto  "))
	require.Equal(t, "crypto", repo.rows[0].Name)
}

func TestAddTopic_RejectsEmptyName(t *testing.T) {
	repo := &fakeRepo{}
	emb := &fakeEmb{vec: nonZeroVec()}
	svc := NewService(Deps{Repo: repo, Embedder: emb})

	err := svc.AddTopic(context.Background(), 1, "   ")
	require.ErrorIs(t, err, ErrEmptyName)
	require.Empty(t, repo.rows)
	require.Equal(t, 0, emb.calls, "empty name must not pay an embed call")
}

func TestAddTopic_LimitReached(t *testing.T) {
	repo := &fakeRepo{}
	emb := &fakeEmb{vec: nonZeroVec()}
	svc := NewService(Deps{Repo: repo, Embedder: emb, MaxTopics: 3})

	for i := 0; i < 3; i++ {
		require.NoError(t, svc.AddTopic(context.Background(), 1, string(rune('a'+i))))
	}
	err := svc.AddTopic(context.Background(), 1, "overflow")
	require.ErrorIs(t, err, ErrTooManyTopics)
	require.Equal(t, 3, emb.calls, "limit-rejected call must not pay an extra embed")
}

func TestAddTopic_EmbedderError(t *testing.T) {
	repo := &fakeRepo{}
	emb := &fakeEmb{err: errors.New("upstream 502")}
	svc := NewService(Deps{Repo: repo, Embedder: emb})

	err := svc.AddTopic(context.Background(), 1, "ai")
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrTooManyTopics)
	require.Empty(t, repo.rows)
}

func TestAddTopic_EmptyEmbedding(t *testing.T) {
	repo := &fakeRepo{}
	emb := &fakeEmb{vec: nil}
	svc := NewService(Deps{Repo: repo, Embedder: emb})

	err := svc.AddTopic(context.Background(), 1, "ai")
	require.ErrorIs(t, err, ErrEmptyEmbedding)
}

func TestRemoveTopic_Idempotent(t *testing.T) {
	repo := &fakeRepo{}
	emb := &fakeEmb{vec: nonZeroVec()}
	svc := NewService(Deps{Repo: repo, Embedder: emb})

	require.NoError(t, svc.AddTopic(context.Background(), 1, "ai"))
	require.NoError(t, svc.RemoveTopic(context.Background(), 1, "ai"))
	require.NoError(t, svc.RemoveTopic(context.Background(), 1, "ai"))
	require.Len(t, repo.rows, 0)
}

func TestListTopics_ReturnsNames(t *testing.T) {
	repo := &fakeRepo{}
	emb := &fakeEmb{vec: nonZeroVec()}
	svc := NewService(Deps{Repo: repo, Embedder: emb})

	require.NoError(t, svc.AddTopic(context.Background(), 1, "ai"))
	require.NoError(t, svc.AddTopic(context.Background(), 1, "crypto"))
	names, err := svc.ListTopics(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, []string{"ai", "crypto"}, names)
}

// TestMatchesAny_NoTopics returns true so the assembler treats a user
// with no custom topics as "no filter" — the default behavior the plan
// calls out for Free users.
func TestMatchesAny_NoTopics_True(t *testing.T) {
	repo := &fakeRepo{matchesAnyResult: false}
	svc := NewService(Deps{Repo: repo, Embedder: &fakeEmb{vec: nonZeroVec()}})

	ok, err := svc.MatchesAny(context.Background(), 1, pgvector.NewVector(nonZeroVec()))
	require.NoError(t, err)
	require.True(t, ok, "user with no topics must pass through (default behavior)")
	require.Equal(t, 0, repo.matchesAnyCalls, "no topics → repo MatchesAny must not be called")
}

func TestMatchesAny_WithTopics_PassesThroughRepo(t *testing.T) {
	repo := &fakeRepo{matchesAnyResult: true}
	emb := &fakeEmb{vec: nonZeroVec()}
	svc := NewService(Deps{Repo: repo, Embedder: emb})

	require.NoError(t, svc.AddTopic(context.Background(), 1, "ai"))

	ok, err := svc.MatchesAny(context.Background(), 1, pgvector.NewVector(nonZeroVec()))
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, 1, repo.matchesAnyCalls)
}

func TestMatchesAny_WithTopics_NoMatch(t *testing.T) {
	repo := &fakeRepo{matchesAnyResult: false}
	emb := &fakeEmb{vec: nonZeroVec()}
	svc := NewService(Deps{Repo: repo, Embedder: emb})

	require.NoError(t, svc.AddTopic(context.Background(), 1, "ai"))

	ok, err := svc.MatchesAny(context.Background(), 1, pgvector.NewVector(nonZeroVec()))
	require.NoError(t, err)
	require.False(t, ok)
	require.Equal(t, 1, repo.matchesAnyCalls)
}

func TestMatchesAny_RepoError(t *testing.T) {
	repo := &fakeRepo{matchesAnyErr: errors.New("db down")}
	emb := &fakeEmb{vec: nonZeroVec()}
	svc := NewService(Deps{Repo: repo, Embedder: emb})

	require.NoError(t, svc.AddTopic(context.Background(), 1, "ai"))
	_, err := svc.MatchesAny(context.Background(), 1, pgvector.NewVector(nonZeroVec()))
	require.Error(t, err)
}
