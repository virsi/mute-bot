package digest

import (
	"context"
	"strings"
	"testing"
	"time"

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
