package dedup

import (
	"context"
	"testing"

	"github.com/pgvector/pgvector-go"
	"github.com/stretchr/testify/require"

	"github.com/virsi/mute-bot/internal/queue"
	"github.com/virsi/mute-bot/internal/storage/postgres"
)

type stubMHIndex struct {
	added      []int64
	candidates []int64
}

func (s *stubMHIndex) Add(_ context.Context, id int64, _ []uint32) error {
	s.added = append(s.added, id)
	return nil
}

func (s *stubMHIndex) Candidates(_ context.Context, _ []uint32, _ int) ([]int64, error) {
	return s.candidates, nil
}

type stubEmbedder struct {
	v   []float32
	err error
}

func (s *stubEmbedder) EmbedOne(_ context.Context, _ string, _ [32]byte) ([]float32, error) {
	return s.v, s.err
}

type stubEmbStore struct {
	near    []postgres.Neighbor
	stored  bool
	storeID int64
}

func (s *stubEmbStore) Store(_ context.Context, postID int64, _ pgvector.Vector, _ string) error {
	s.stored = true
	s.storeID = postID
	return nil
}

func (s *stubEmbStore) NearestNeighbors(_ context.Context, _ pgvector.Vector, _ postgres.NearestParams) ([]postgres.Neighbor, error) {
	return s.near, nil
}

type stubClusters struct {
	created int64
	covered int64
}

func (s *stubClusters) Create(_ context.Context) (int64, error) {
	s.created++
	return s.created, nil
}

func (s *stubClusters) IncrementCoverage(_ context.Context, id int64) error {
	s.covered = id
	return nil
}

type stubPosts struct {
	clusterByID map[int64]int64
	attached    map[int64]int64
}

func newStubPosts() *stubPosts {
	return &stubPosts{
		clusterByID: map[int64]int64{},
		attached:    map[int64]int64{},
	}
}

func (s *stubPosts) GetClusterID(_ context.Context, postID int64) (int64, error) {
	return s.clusterByID[postID], nil
}

func (s *stubPosts) AttachCluster(_ context.Context, postID, clusterID int64) error {
	s.attached[postID] = clusterID
	s.clusterByID[postID] = clusterID
	return nil
}

type stubPub struct {
	subjects []string
}

func (s *stubPub) Publish(_ context.Context, subject string, _ any) error {
	s.subjects = append(s.subjects, subject)
	return nil
}

func TestMatcher_MinHashHit_AttachesExistingCluster(t *testing.T) {
	posts := newStubPosts()
	posts.clusterByID[777] = 42

	mh := &stubMHIndex{candidates: []int64{777}}
	emb := &stubEmbedder{v: []float32{0.1}}
	store := &stubEmbStore{}
	cls := &stubClusters{}
	pub := &stubPub{}

	m := NewMatcher(MatcherDeps{
		MinHashIndex: mh,
		Embedder:     emb,
		Embeddings:   store,
		Clusters:     cls,
		Posts:        posts,
		Publisher:    pub,
	})

	require.NoError(t, m.Match(context.Background(), MatchInput{
		PostID: 1, TextClean: "hello world", Hash: [32]byte{1},
	}))
	require.Equal(t, int64(42), posts.attached[1])
	require.Equal(t, int64(42), cls.covered)
	require.False(t, store.stored, "should not call embedder/store on minhash hit")
	require.Contains(t, pub.subjects, queue.SubjectClusterUpdate)
	require.Equal(t, []int64{1}, mh.added)
}

func TestMatcher_MinHashCandidateWithoutCluster_FallsThroughToEmbedding(t *testing.T) {
	// Candidate post exists but has no cluster yet — matcher must not crash
	// and must proceed to embedding lookup.
	posts := newStubPosts()
	mh := &stubMHIndex{candidates: []int64{555}}
	store := &stubEmbStore{near: nil}
	cls := &stubClusters{}
	pub := &stubPub{}

	m := NewMatcher(MatcherDeps{
		MinHashIndex: mh,
		Embedder:     &stubEmbedder{v: []float32{0.1}},
		Embeddings:   store,
		Clusters:     cls,
		Posts:        posts,
		Publisher:    pub,
	})
	require.NoError(t, m.Match(context.Background(), MatchInput{
		PostID: 1, TextClean: "x", Hash: [32]byte{1},
	}))
	require.True(t, store.stored)
	require.Equal(t, int64(1), posts.attached[1]) // new cluster id
	require.Contains(t, pub.subjects, queue.SubjectClusterUpdate)
}

func TestMatcher_EmbeddingHit_AttachesExistingCluster(t *testing.T) {
	posts := newStubPosts()
	posts.clusterByID[999] = 7

	mh := &stubMHIndex{candidates: nil}
	store := &stubEmbStore{near: []postgres.Neighbor{{PostID: 999, Distance: 0.05}}}
	cls := &stubClusters{}
	pub := &stubPub{}

	m := NewMatcher(MatcherDeps{
		MinHashIndex: mh,
		Embedder:     &stubEmbedder{v: []float32{0.1}},
		Embeddings:   store,
		Clusters:     cls,
		Posts:        posts,
		Publisher:    pub,
	})
	require.NoError(t, m.Match(context.Background(), MatchInput{
		PostID: 1, TextClean: "x", Hash: [32]byte{2},
	}))
	require.Equal(t, int64(7), posts.attached[1])
	require.Equal(t, int64(7), cls.covered)
	require.True(t, store.stored)
	require.Contains(t, pub.subjects, queue.SubjectClusterUpdate)
}

func TestMatcher_NoCandidates_CreatesNewCluster(t *testing.T) {
	posts := newStubPosts()
	mh := &stubMHIndex{candidates: nil}
	cls := &stubClusters{}
	pub := &stubPub{}
	store := &stubEmbStore{near: nil}

	m := NewMatcher(MatcherDeps{
		MinHashIndex: mh,
		Embedder:     &stubEmbedder{v: []float32{0.1}},
		Embeddings:   store,
		Clusters:     cls,
		Posts:        posts,
		Publisher:    pub,
	})
	require.NoError(t, m.Match(context.Background(), MatchInput{
		PostID: 1, TextClean: "fresh news", Hash: [32]byte{2},
	}))
	require.Equal(t, int64(1), posts.attached[1])
	require.Equal(t, int64(1), cls.created)
	require.Contains(t, pub.subjects, queue.SubjectClusterUpdate)
	require.Equal(t, []int64{1}, mh.added)
}

func TestMatcher_EmbeddingNeighborSelf_Skipped(t *testing.T) {
	// kNN may return the post we just inserted; ensure the matcher ignores
	// self-matches.
	posts := newStubPosts()
	mh := &stubMHIndex{candidates: nil}
	store := &stubEmbStore{near: []postgres.Neighbor{{PostID: 1, Distance: 0.0}}}
	cls := &stubClusters{}
	pub := &stubPub{}

	m := NewMatcher(MatcherDeps{
		MinHashIndex: mh,
		Embedder:     &stubEmbedder{v: []float32{0.1}},
		Embeddings:   store,
		Clusters:     cls,
		Posts:        posts,
		Publisher:    pub,
	})
	require.NoError(t, m.Match(context.Background(), MatchInput{
		PostID: 1, TextClean: "fresh news", Hash: [32]byte{2},
	}))
	require.Equal(t, int64(1), cls.created) // new cluster, self-match ignored
	require.Equal(t, int64(1), posts.attached[1])
}
