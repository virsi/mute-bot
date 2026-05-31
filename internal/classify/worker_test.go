package classify

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/virsi/mute-bot/internal/queue"
)

type stubPosts struct {
	texts map[int64][]string
	lang  string
	err   error
}

func (s *stubPosts) ListTextsByCluster(_ context.Context, clusterID int64) ([]string, string, error) {
	if s.err != nil {
		return nil, "", s.err
	}
	lang := s.lang
	if lang == "" {
		lang = "ru"
	}
	return s.texts[clusterID], lang, nil
}

type stubClassifier struct {
	res Result
	err error

	mu       sync.Mutex
	calls    int
	lastTxts []string
	lastLang string
}

func (s *stubClassifier) Classify(_ context.Context, posts []string, lang string) (Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.lastTxts = posts
	s.lastLang = lang
	return s.res, s.err
}

func (s *stubClassifier) Calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

type stubClusters struct {
	calls int32

	mu    sync.Mutex
	saved map[int64]MetaUpdate
	err   error
}

func (s *stubClusters) UpdateMeta(_ context.Context, id int64, m MetaUpdate) error {
	atomic.AddInt32(&s.calls, 1)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	if s.saved == nil {
		s.saved = make(map[int64]MetaUpdate)
	}
	s.saved[id] = m
	return nil
}

func (s *stubClusters) get(id int64) MetaUpdate {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saved[id]
}

type stubPub struct {
	mu       sync.Mutex
	subjects []string
	payloads []any
	err      error
}

func (s *stubPub) Publish(_ context.Context, subj string, payload any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.subjects = append(s.subjects, subj)
	s.payloads = append(s.payloads, payload)
	return nil
}

func (s *stubPub) snapshot() ([]string, []any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	subj := append([]string(nil), s.subjects...)
	pay := append([]any(nil), s.payloads...)
	return subj, pay
}

func newWorkerForTest(t *testing.T, debounce time.Duration,
	cls *stubClassifier, posts *stubPosts, clusters *stubClusters, pub *stubPub,
) *Worker {
	t.Helper()
	return NewWorker(WorkerDeps{
		Classifier: cls,
		Posts:      posts,
		Clusters:   clusters,
		Publisher:  pub,
		Debounce:   debounce,
	})
}

func TestWorker_Debounces_CollapsesMultipleEventsIntoOneCall(t *testing.T) {
	cls := &stubClassifier{res: Result{Topics: []string{"it"}, Severity: 60, Headline: "h", Summary: "s"}}
	posts := &stubPosts{texts: map[int64][]string{42: {"hello world"}}}
	clusters := &stubClusters{}
	pub := &stubPub{}
	w := newWorkerForTest(t, 50*time.Millisecond, cls, posts, clusters, pub)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	evt, _ := json.Marshal(map[string]any{"cluster_id": 42})
	for i := 0; i < 5; i++ {
		require.NoError(t, w.Handle(ctx, evt))
		time.Sleep(5 * time.Millisecond)
	}
	require.Eventually(t, func() bool {
		return atomic.LoadInt32(&clusters.calls) == 1
	}, time.Second, 10*time.Millisecond)

	require.Equal(t, 1, cls.Calls())
	m := clusters.get(42)
	require.Equal(t, []string{"it"}, m.Topics)
	require.Equal(t, 60, m.Severity)
	require.Equal(t, "h", m.Headline)

	subj, pay := pub.snapshot()
	require.Equal(t, []string{queue.SubjectClusterScored}, subj)
	require.Len(t, pay, 1)
	payload, ok := pay[0].(map[string]any)
	require.True(t, ok)
	require.EqualValues(t, 42, payload["cluster_id"])
}

func TestWorker_Handle_RejectsInvalidJSON(t *testing.T) {
	w := newWorkerForTest(t, 50*time.Millisecond,
		&stubClassifier{}, &stubPosts{}, &stubClusters{}, &stubPub{})
	require.Error(t, w.Handle(context.Background(), []byte("not json")))
}

func TestWorker_Handle_RejectsZeroClusterID(t *testing.T) {
	w := newWorkerForTest(t, 50*time.Millisecond,
		&stubClassifier{}, &stubPosts{}, &stubClusters{}, &stubPub{})
	evt, _ := json.Marshal(map[string]any{"cluster_id": 0})
	require.Error(t, w.Handle(context.Background(), evt))
}

func TestWorker_SkipsEmptyClusters(t *testing.T) {
	cls := &stubClassifier{}
	posts := &stubPosts{texts: map[int64][]string{}}
	clusters := &stubClusters{}
	pub := &stubPub{}
	w := newWorkerForTest(t, 30*time.Millisecond, cls, posts, clusters, pub)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	evt, _ := json.Marshal(map[string]any{"cluster_id": 99})
	require.NoError(t, w.Handle(ctx, evt))

	time.Sleep(100 * time.Millisecond)
	require.Equal(t, 0, cls.Calls())
	require.Equal(t, int32(0), atomic.LoadInt32(&clusters.calls))
	subj, _ := pub.snapshot()
	require.Empty(t, subj)
}

func TestWorker_PassesLangFromPosts(t *testing.T) {
	cls := &stubClassifier{res: Result{Headline: "h", Summary: "s"}}
	posts := &stubPosts{texts: map[int64][]string{1: {"text"}}, lang: "en"}
	w := newWorkerForTest(t, 30*time.Millisecond, cls, posts, &stubClusters{}, &stubPub{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	evt, _ := json.Marshal(map[string]any{"cluster_id": 1})
	require.NoError(t, w.Handle(ctx, evt))

	require.Eventually(t, func() bool { return cls.Calls() == 1 },
		time.Second, 10*time.Millisecond)
	cls.mu.Lock()
	defer cls.mu.Unlock()
	require.Equal(t, "en", cls.lastLang)
	require.Equal(t, []string{"text"}, cls.lastTxts)
}

func TestClustersUpdaterFunc_Adapts(t *testing.T) {
	var got MetaUpdate
	var gotID int64
	f := ClustersUpdaterFunc(func(_ context.Context, id int64, m MetaUpdate) error {
		gotID = id
		got = m
		return nil
	})
	var iface ClustersUpdater = f
	require.NoError(t, iface.UpdateMeta(context.Background(), 7,
		MetaUpdate{Headline: "x", Severity: 11}))
	require.Equal(t, int64(7), gotID)
	require.Equal(t, "x", got.Headline)
	require.Equal(t, 11, got.Severity)
}

func TestWorker_DefaultDebounceWhenZero(t *testing.T) {
	w := NewWorker(WorkerDeps{
		Classifier: &stubClassifier{},
		Posts:      &stubPosts{},
		Clusters:   &stubClusters{},
		Publisher:  &stubPub{},
	})
	require.Equal(t, defaultDebounce, w.debounce)
}

func TestWorker_DoesNotPublish_WhenUpdateMetaFails(t *testing.T) {
	cls := &stubClassifier{res: Result{Headline: "h", Summary: "s"}}
	posts := &stubPosts{texts: map[int64][]string{5: {"t"}}}
	clusters := &stubClusters{err: errors.New("db down")}
	pub := &stubPub{}
	w := newWorkerForTest(t, 30*time.Millisecond, cls, posts, clusters, pub)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	evt, _ := json.Marshal(map[string]any{"cluster_id": 5})
	require.NoError(t, w.Handle(ctx, evt))

	time.Sleep(150 * time.Millisecond)
	subj, _ := pub.snapshot()
	require.Empty(t, subj)
}
