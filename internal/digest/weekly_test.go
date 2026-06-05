package digest

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/virsi/mute-bot/internal/storage/postgres"
)

// stubTopReader is the unit-test double for WeeklyTopReader.
type stubTopReader struct {
	list       []postgres.Cluster
	gotSince   time.Time
	gotTopics  []string
	gotExclude []int64
	gotLimit   int
	err        error
}

func (s *stubTopReader) TopByScoreSince(_ context.Context, since time.Time,
	topics []string, exclude []int64, limit int,
) ([]postgres.Cluster, error) {
	s.gotSince = since
	s.gotTopics = topics
	s.gotExclude = exclude
	s.gotLimit = limit
	return s.list, s.err
}

// stubWeeklyRepo is the unit-test double for WeeklyRepo.
type stubWeeklyRepo struct {
	has        bool
	hasErr     error
	excluded   []int64
	exclErr    error
	inserted   []int64
	insertedAt map[int64]string
	insertErr  error
}

func (s *stubWeeklyRepo) InsertIfAbsent(_ context.Context, _ int64, cid int64, isoWeek string) (bool, error) {
	if s.insertErr != nil {
		return false, s.insertErr
	}
	if s.insertedAt == nil {
		s.insertedAt = make(map[int64]string)
	}
	s.inserted = append(s.inserted, cid)
	s.insertedAt[cid] = isoWeek
	return true, nil
}

func (s *stubWeeklyRepo) HasWeekRow(_ context.Context, _ int64, _ string) (bool, error) {
	return s.has, s.hasErr
}

func (s *stubWeeklyRepo) ListClusterIDsSince(_ context.Context, _ int64, _ time.Time, _ int) ([]int64, error) {
	return s.excluded, s.exclErr
}

func TestWeeklyAssembler_HappyPath_SendsAndRecordsRows(t *testing.T) {
	now := time.Date(2026, 6, 7, 18, 0, 0, 0, time.UTC) // Sunday — ISO week 2026-23

	sender := &stubSender{}
	weekly := &stubWeeklyRepo{}
	clusters := &stubTopReader{list: []postgres.Cluster{
		{ID: 11, Headline: "h1", Summary: "s1", Topics: []string{"politics"}, Score: 0.9},
		{ID: 12, Headline: "h2", Summary: "s2", Topics: []string{"politics"}, Score: 0.8},
	}}
	sources := &stubSources{srcs: map[int64][]string{11: {"a"}, 12: {"b"}}}

	asm := NewWeeklyAssembler(WeeklyAssemblerDeps{
		Settings: &stubSettings{s: postgres.Settings{
			Topics: []string{"politics"}, Threshold: 50,
		}},
		Clusters: clusters,
		Weekly:   weekly,
		Sources:  sources,
		Sender:   sender,
		Now:      func() time.Time { return now },
	})

	require.NoError(t, asm.BuildWeekly(context.Background(), WeeklyRequest{UserID: 1, TGUserID: 100}))
	require.Len(t, sender.messages, 1)
	require.Contains(t, sender.messages[0], "Главное за неделю")
	require.Contains(t, sender.messages[0], "h1")
	require.Contains(t, sender.messages[0], "h2")
	require.ElementsMatch(t, []int64{11, 12}, weekly.inserted)
	require.Equal(t, "2026-23", weekly.insertedAt[11])
	require.Equal(t, []string{"politics"}, clusters.gotTopics)
	require.Equal(t, 10, clusters.gotLimit)
	// since must be ~7 days ago.
	require.WithinDuration(t, now.Add(-7*24*time.Hour), clusters.gotSince, time.Second)
}

func TestWeeklyAssembler_AlreadySentThisWeek_NoOp(t *testing.T) {
	now := time.Date(2026, 6, 7, 18, 0, 0, 0, time.UTC)
	sender := &stubSender{}
	asm := NewWeeklyAssembler(WeeklyAssemblerDeps{
		Settings: &stubSettings{s: postgres.Settings{Topics: []string{"politics"}}},
		Clusters: &stubTopReader{},
		Weekly:   &stubWeeklyRepo{has: true},
		Sources:  &stubSources{},
		Sender:   sender,
		Now:      func() time.Time { return now },
	})
	require.NoError(t, asm.BuildWeekly(context.Background(), WeeklyRequest{UserID: 1, TGUserID: 100}))
	require.Empty(t, sender.messages, "anti-repeat must block second send in same week")
}

func TestWeeklyAssembler_EmptyResult_NoSend(t *testing.T) {
	now := time.Date(2026, 6, 7, 18, 0, 0, 0, time.UTC)
	sender := &stubSender{}
	weekly := &stubWeeklyRepo{}
	asm := NewWeeklyAssembler(WeeklyAssemblerDeps{
		Settings: &stubSettings{s: postgres.Settings{Topics: []string{"politics"}}},
		Clusters: &stubTopReader{list: nil},
		Weekly:   weekly,
		Sources:  &stubSources{},
		Sender:   sender,
		Now:      func() time.Time { return now },
	})
	require.NoError(t, asm.BuildWeekly(context.Background(), WeeklyRequest{UserID: 1, TGUserID: 100}))
	require.Empty(t, sender.messages)
	require.Empty(t, weekly.inserted)
}

func TestWeeklyAssembler_AntiRepeat_PreviousWeekExcluded(t *testing.T) {
	now := time.Date(2026, 6, 7, 18, 0, 0, 0, time.UTC)
	sender := &stubSender{}
	weekly := &stubWeeklyRepo{excluded: []int64{777}}
	clusters := &stubTopReader{list: []postgres.Cluster{
		{ID: 11, Headline: "h1", Topics: []string{"politics"}, Score: 0.9},
	}}
	asm := NewWeeklyAssembler(WeeklyAssemblerDeps{
		Settings: &stubSettings{s: postgres.Settings{Topics: []string{"politics"}}},
		Clusters: clusters,
		Weekly:   weekly,
		Sources:  &stubSources{},
		Sender:   sender,
		Now:      func() time.Time { return now },
	})
	require.NoError(t, asm.BuildWeekly(context.Background(), WeeklyRequest{UserID: 1, TGUserID: 100}))
	require.Equal(t, []int64{777}, clusters.gotExclude, "exclusion list passed through to repo")
	require.Len(t, sender.messages, 1)
}

// TestWeeklyAssembler_OnDemand_AlreadyDelivered pins the post-review
// contract: when /weekly is invoked on-demand and the user has already
// received this ISO week's digest, BuildWeekly is a no-op (no second
// send). The HasWeekRow check is unconditional; on-demand no longer
// bypasses it because cron + /weekly otherwise produced two messages
// for the same week.
func TestWeeklyAssembler_OnDemand_AlreadyDelivered(t *testing.T) {
	now := time.Date(2026, 6, 7, 18, 0, 0, 0, time.UTC)
	sender := &stubSender{}
	weekly := &stubWeeklyRepo{has: true}
	clusters := &stubTopReader{list: []postgres.Cluster{
		{ID: 11, Headline: "h1", Topics: []string{"politics"}, Score: 0.9},
	}}
	asm := NewWeeklyAssembler(WeeklyAssemblerDeps{
		Settings: &stubSettings{s: postgres.Settings{Topics: []string{"politics"}}},
		Clusters: clusters,
		Weekly:   weekly,
		Sources:  &stubSources{},
		Sender:   sender,
		Now:      func() time.Time { return now },
	})
	require.NoError(t, asm.BuildWeekly(context.Background(), WeeklyRequest{UserID: 1, TGUserID: 100}))
	require.Empty(t, sender.messages, "no second send when HasWeekRow=true")
	require.Empty(t, weekly.inserted, "no extra row recorded")
}

func TestWeeklyAssembler_TitleContainsDateRange(t *testing.T) {
	now := time.Date(2026, 6, 7, 18, 0, 0, 0, time.UTC)
	sender := &stubSender{}
	asm := NewWeeklyAssembler(WeeklyAssemblerDeps{
		Settings: &stubSettings{s: postgres.Settings{Topics: []string{"politics"}}},
		Clusters: &stubTopReader{list: []postgres.Cluster{
			{ID: 11, Headline: "h1", Topics: []string{"politics"}, Score: 0.9},
		}},
		Weekly:  &stubWeeklyRepo{},
		Sources: &stubSources{},
		Sender:  sender,
		Now:     func() time.Time { return now },
	})
	require.NoError(t, asm.BuildWeekly(context.Background(), WeeklyRequest{UserID: 1, TGUserID: 100}))
	require.Len(t, sender.messages, 1)
	// Date range: 31.05 — 07.06.2026 (UTC).
	require.True(t,
		strings.Contains(sender.messages[0], "31.05") &&
			strings.Contains(sender.messages[0], "07.06.2026"),
		"digest header must contain the date range, got: %q", sender.messages[0])
}

func TestISOWeekKey_PadsZero(t *testing.T) {
	require.Equal(t, "2026-23", ISOWeekKey(time.Date(2026, 6, 7, 18, 0, 0, 0, time.UTC)))
	// Jan 1, 2026 falls in ISO week 1 — verify the zero-pad on the week
	// component is present.
	require.Equal(t, "2026-01", ISOWeekKey(time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)))
}
