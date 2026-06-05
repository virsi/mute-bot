package digest

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pgvector/pgvector-go"
	"github.com/stretchr/testify/require"

	"github.com/virsi/mute-bot/internal/storage/postgres"
)

type stubSettings struct {
	s   postgres.Settings
	err error
}

func (s *stubSettings) Get(_ context.Context, _ int64) (postgres.Settings, error) {
	return s.s, s.err
}

type stubClusters struct {
	list      []postgres.Cluster
	gotFilter postgres.ClusterFilter
	err       error
}

func (s *stubClusters) Search(_ context.Context, f postgres.ClusterFilter) ([]postgres.Cluster, error) {
	s.gotFilter = f
	return s.list, s.err
}

type stubDelivs struct {
	excluded []int64
	saved    []int64
	channel  string
}

func (s *stubDelivs) ListClusterIDs(_ context.Context, _ int64, _ int) ([]int64, error) {
	return s.excluded, nil
}

func (s *stubDelivs) Record(_ context.Context, _, cid int64, ch string) error {
	s.saved = append(s.saved, cid)
	s.channel = ch
	return nil
}

type stubSources struct {
	srcs map[int64][]string
}

func (s *stubSources) SourcesForCluster(_ context.Context, cid int64) ([]string, error) {
	return s.srcs[cid], nil
}

type stubSender struct {
	messages []string
	chats    []int64
	err      error
}

func (s *stubSender) SendDigest(_ context.Context, chatID int64, text string) error {
	s.chats = append(s.chats, chatID)
	s.messages = append(s.messages, text)
	return s.err
}

func TestAssembler_BuildsAndSends(t *testing.T) {
	sender := &stubSender{}
	delivs := &stubDelivs{}
	a := NewAssembler(AssemblerDeps{
		Settings:   &stubSettings{s: postgres.Settings{Topics: []string{"politics"}, Threshold: 50}},
		Clusters:   &stubClusters{list: []postgres.Cluster{{ID: 11, Headline: "h", Summary: "s", Topics: []string{"politics"}, Score: 80}}},
		Deliveries: delivs,
		Sources:    &stubSources{srcs: map[int64][]string{11: {"ria"}}},
		Sender:     sender,
		Now:        func() time.Time { return time.Date(2026, 6, 3, 8, 0, 0, 0, time.UTC) },
	})
	require.NoError(t, a.Assemble(context.Background(), AssembleRequest{
		UserID:   1,
		TGUserID: 100,
		Channel:  "digest",
		Title:    "Утренняя сводка",
	}))
	require.Len(t, sender.messages, 1)
	require.Contains(t, sender.messages[0], "Утренняя сводка")
	require.Contains(t, sender.messages[0], "h")
	require.Equal(t, []int64{100}, sender.chats)
	require.Equal(t, []int64{11}, delivs.saved, "delivered cluster must be recorded")
	require.Equal(t, "digest", delivs.channel)
}

func TestAssembler_AppliesFilter(t *testing.T) {
	clusters := &stubClusters{}
	a := NewAssembler(AssemblerDeps{
		Settings:   &stubSettings{s: postgres.Settings{Topics: []string{"politics", "crypto"}, Threshold: 60}},
		Clusters:   clusters,
		Deliveries: &stubDelivs{excluded: []int64{5, 7}},
		Sources:    &stubSources{},
		Sender:     &stubSender{},
		Now:        func() time.Time { return time.Date(2026, 6, 3, 8, 0, 0, 0, time.UTC) },
	})
	_ = a.Assemble(context.Background(), AssembleRequest{UserID: 1, TGUserID: 100, Channel: "digest"})

	require.Equal(t, []string{"politics", "crypto"}, clusters.gotFilter.Topics)
	require.InDelta(t, 0.6, clusters.gotFilter.MinScore, 0.001)
	require.Equal(t, []int64{5, 7}, clusters.gotFilter.ExcludeIDs)
	require.False(t, clusters.gotFilter.SinceTime.IsZero())
}

func TestAssembler_NoClustersDoesNotSend(t *testing.T) {
	sender := &stubSender{}
	delivs := &stubDelivs{}
	a := NewAssembler(AssemblerDeps{
		Settings:   &stubSettings{s: postgres.Settings{Topics: []string{"politics"}, Threshold: 50}},
		Clusters:   &stubClusters{list: nil},
		Deliveries: delivs,
		Sources:    &stubSources{},
		Sender:     sender,
		Now:        func() time.Time { return time.Date(2026, 6, 3, 8, 0, 0, 0, time.UTC) },
	})
	require.NoError(t, a.Assemble(context.Background(), AssembleRequest{UserID: 1, TGUserID: 100, Channel: "digest"}))
	require.Empty(t, sender.messages, "empty digest must not be sent")
	require.Empty(t, delivs.saved, "nothing to record when nothing was sent")
}

// --- Custom-topic filter (M4) ---

type stubUsers struct {
	u   postgres.User
	err error
}

func (s *stubUsers) GetByID(_ context.Context, _ int64) (postgres.User, error) {
	return s.u, s.err
}

type stubTier struct{ pro bool }

func (s *stubTier) IsPro(_ postgres.User) bool { return s.pro }

type stubCentroider struct {
	vec pgvector.Vector
	err error
}

func (s *stubCentroider) ClusterCentroid(_ context.Context, _ int64) (pgvector.Vector, error) {
	return s.vec, s.err
}

type stubTopicMatcher struct {
	answer bool
	calls  int
	err    error
}

func (s *stubTopicMatcher) MatchesAny(_ context.Context, _ int64, _ pgvector.Vector) (bool, error) {
	s.calls++
	if s.err != nil {
		return false, s.err
	}
	return s.answer, nil
}

func nonZeroEmbedding() pgvector.Vector {
	v := make([]float32, 4)
	v[0] = 0.1
	return pgvector.NewVector(v)
}

// TestAssembler_FreeUser_FilterNotApplied verifies that a Free
// recipient never pays for the centroid / topic-match lookup and that
// every cluster from Search reaches Sender. Mirrors the digest path for
// users who have not bought Pro.
func TestAssembler_FreeUser_FilterNotApplied(t *testing.T) {
	sender := &stubSender{}
	delivs := &stubDelivs{}
	tm := &stubTopicMatcher{}
	a := NewAssembler(AssemblerDeps{
		Settings: &stubSettings{s: postgres.Settings{Topics: []string{"politics"}, Threshold: 50}},
		Clusters: &stubClusters{list: []postgres.Cluster{
			{ID: 11, Headline: "h1", Summary: "s", Topics: []string{"politics"}, Score: 80},
		}},
		Deliveries:   delivs,
		Sources:      &stubSources{},
		Sender:       sender,
		CustomTopics: tm,
		Centroider:   &stubCentroider{vec: nonZeroEmbedding()},
		Tier:         &stubTier{pro: false},
		Users:        &stubUsers{u: postgres.User{Tier: "free"}},
		Now:          func() time.Time { return time.Date(2026, 6, 3, 8, 0, 0, 0, time.UTC) },
	})
	require.NoError(t, a.Assemble(context.Background(), AssembleRequest{
		UserID: 1, TGUserID: 100, Channel: "digest",
	}))
	require.Equal(t, 0, tm.calls, "Free user must not pay for the topic-match query")
	require.Equal(t, []int64{11}, delivs.saved, "every cluster must pass through for Free users")
}

// TestAssembler_ProUser_MatchKeepsCluster shows the happy Pro path: the
// matcher returns true for the cluster centroid, so it stays in the
// digest.
func TestAssembler_ProUser_MatchKeepsCluster(t *testing.T) {
	sender := &stubSender{}
	delivs := &stubDelivs{}
	tm := &stubTopicMatcher{answer: true}
	a := NewAssembler(AssemblerDeps{
		Settings: &stubSettings{s: postgres.Settings{Topics: []string{"politics"}, Threshold: 50}},
		Clusters: &stubClusters{list: []postgres.Cluster{
			{ID: 11, Headline: "h1", Summary: "s", Topics: []string{"politics"}, Score: 80},
		}},
		Deliveries:   delivs,
		Sources:      &stubSources{},
		Sender:       sender,
		CustomTopics: tm,
		Centroider:   &stubCentroider{vec: nonZeroEmbedding()},
		Tier:         &stubTier{pro: true},
		Users:        &stubUsers{u: postgres.User{Tier: "pro"}},
		Now:          func() time.Time { return time.Date(2026, 6, 3, 8, 0, 0, 0, time.UTC) },
	})
	require.NoError(t, a.Assemble(context.Background(), AssembleRequest{
		UserID: 1, TGUserID: 100, Channel: "digest",
	}))
	require.Equal(t, 1, tm.calls, "Pro user must consult the matcher once per cluster")
	require.Equal(t, []int64{11}, delivs.saved)
}

// TestAssembler_ProUser_NoMatchDropsCluster shows the rejection path: a
// Pro user with custom topics that the matcher rules out for the only
// candidate cluster gets an empty digest (no send, no delivery record).
func TestAssembler_ProUser_NoMatchDropsCluster(t *testing.T) {
	sender := &stubSender{}
	delivs := &stubDelivs{}
	tm := &stubTopicMatcher{answer: false}
	a := NewAssembler(AssemblerDeps{
		Settings: &stubSettings{s: postgres.Settings{Topics: []string{"politics"}, Threshold: 50}},
		Clusters: &stubClusters{list: []postgres.Cluster{
			{ID: 11, Headline: "h1", Summary: "s", Topics: []string{"politics"}, Score: 80},
		}},
		Deliveries:   delivs,
		Sources:      &stubSources{},
		Sender:       sender,
		CustomTopics: tm,
		Centroider:   &stubCentroider{vec: nonZeroEmbedding()},
		Tier:         &stubTier{pro: true},
		Users:        &stubUsers{u: postgres.User{Tier: "pro"}},
		Now:          func() time.Time { return time.Date(2026, 6, 3, 8, 0, 0, 0, time.UTC) },
	})
	require.NoError(t, a.Assemble(context.Background(), AssembleRequest{
		UserID: 1, TGUserID: 100, Channel: "digest",
	}))
	require.Empty(t, sender.messages, "no message when every cluster is filtered out")
	require.Empty(t, delivs.saved, "no delivery recorded when nothing was sent")
}

// TestAssembler_ProUser_NoEmbeddingsKeepsCluster proves the
// ErrNoEmbeddings fall-through: a fresh cluster without post embeddings
// still reaches the user instead of being silently dropped.
func TestAssembler_ProUser_NoEmbeddingsKeepsCluster(t *testing.T) {
	sender := &stubSender{}
	delivs := &stubDelivs{}
	tm := &stubTopicMatcher{answer: false}
	a := NewAssembler(AssemblerDeps{
		Settings: &stubSettings{s: postgres.Settings{Topics: []string{"politics"}, Threshold: 50}},
		Clusters: &stubClusters{list: []postgres.Cluster{
			{ID: 11, Headline: "h1", Summary: "s", Topics: []string{"politics"}, Score: 80},
		}},
		Deliveries:   delivs,
		Sources:      &stubSources{},
		Sender:       sender,
		CustomTopics: tm,
		Centroider:   &stubCentroider{err: postgres.ErrNoEmbeddings},
		Tier:         &stubTier{pro: true},
		Users:        &stubUsers{u: postgres.User{Tier: "pro"}},
		Now:          func() time.Time { return time.Date(2026, 6, 3, 8, 0, 0, 0, time.UTC) },
	})
	require.NoError(t, a.Assemble(context.Background(), AssembleRequest{
		UserID: 1, TGUserID: 100, Channel: "digest",
	}))
	require.Equal(t, []int64{11}, delivs.saved, "cluster without embeddings must pass through")
	require.Equal(t, 0, tm.calls, "matcher must not be called when centroid is unavailable")
}

// TestAssembler_NoCustomTopicDeps_NoOp catches the partial-wiring case:
// when any of CustomTopics/Centroider/Tier/Users is nil, the assembler
// behaves exactly as in Phase 1.
func TestAssembler_NoCustomTopicDeps_NoOp(t *testing.T) {
	sender := &stubSender{}
	delivs := &stubDelivs{}
	a := NewAssembler(AssemblerDeps{
		Settings: &stubSettings{s: postgres.Settings{Topics: []string{"politics"}, Threshold: 50}},
		Clusters: &stubClusters{list: []postgres.Cluster{
			{ID: 22, Headline: "h2", Summary: "s", Topics: []string{"politics"}, Score: 80},
		}},
		Deliveries: delivs,
		Sources:    &stubSources{},
		Sender:     sender,
		Now:        func() time.Time { return time.Date(2026, 6, 3, 8, 0, 0, 0, time.UTC) },
	})
	require.NoError(t, a.Assemble(context.Background(), AssembleRequest{
		UserID: 1, TGUserID: 100, Channel: "digest",
	}))
	require.Equal(t, []int64{22}, delivs.saved)
}

// TestAssembler_ProUser_MatchErr_DropsCluster confirms that a transient
// matcher error costs only the affected cluster: the assembler keeps
// going, logs a warning, and emits a digest from whatever clusters did
// match.
func TestAssembler_ProUser_MatchErr_DropsCluster(t *testing.T) {
	sender := &stubSender{}
	delivs := &stubDelivs{}
	tm := &stubTopicMatcher{err: errors.New("db down")}
	a := NewAssembler(AssemblerDeps{
		Settings: &stubSettings{s: postgres.Settings{Topics: []string{"politics"}, Threshold: 50}},
		Clusters: &stubClusters{list: []postgres.Cluster{
			{ID: 11, Headline: "h1", Summary: "s", Topics: []string{"politics"}, Score: 80},
		}},
		Deliveries:   delivs,
		Sources:      &stubSources{},
		Sender:       sender,
		CustomTopics: tm,
		Centroider:   &stubCentroider{vec: nonZeroEmbedding()},
		Tier:         &stubTier{pro: true},
		Users:        &stubUsers{u: postgres.User{Tier: "pro"}},
		Now:          func() time.Time { return time.Date(2026, 6, 3, 8, 0, 0, 0, time.UTC) },
	})
	require.NoError(t, a.Assemble(context.Background(), AssembleRequest{
		UserID: 1, TGUserID: 100, Channel: "digest",
	}))
	require.Empty(t, sender.messages)
	require.Empty(t, delivs.saved)
}

func TestAssembler_FallbackHeadline(t *testing.T) {
	sender := &stubSender{}
	a := NewAssembler(AssemblerDeps{
		Settings:   &stubSettings{s: postgres.Settings{Topics: []string{"politics"}, Threshold: 50}},
		Clusters:   &stubClusters{list: []postgres.Cluster{{ID: 1, Headline: "", Summary: "s", Topics: []string{"politics"}}}},
		Deliveries: &stubDelivs{},
		Sources:    &stubSources{},
		Sender:     sender,
		Now:        func() time.Time { return time.Date(2026, 6, 3, 8, 0, 0, 0, time.UTC) },
	})
	require.NoError(t, a.Assemble(context.Background(), AssembleRequest{UserID: 1, TGUserID: 100, Channel: "digest"}))
	require.Len(t, sender.messages, 1)
	require.True(t, strings.Contains(sender.messages[0], "Без заголовка"))
}
