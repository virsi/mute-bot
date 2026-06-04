package alerts

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeUsers returns a fixed eligible list and records ListEligible calls.
// Tests that need the Free-tier invariant assert the worker never asks
// the throttle for a user that was not enumerated here.
type fakeUsers struct {
	list  []EligibleUser
	calls int
	err   error
}

func (f *fakeUsers) ListEligible(_ context.Context) ([]EligibleUser, error) {
	f.calls++
	return f.list, f.err
}

// fakeClusters returns a fixed Cluster. Tests change the fields per case
// to control score / topics / headline.
type fakeClusters struct {
	c   Cluster
	err error
}

func (f *fakeClusters) Get(_ context.Context, _ int64) (Cluster, error) {
	return f.c, f.err
}

// fakeThrottle records per-(user, topic) Allow calls. The allow flag
// drives whether the slot is acquired. When fail is set, Allow returns an
// error to simulate a Redis hiccup.
type fakeThrottle struct {
	allow   bool
	fail    bool
	calls   []throttleCall
	perUser map[int64]bool // when set, overrides allow per user
}

type throttleCall struct {
	UserID int64
	Topic  string
	TTL    time.Duration
}

func (f *fakeThrottle) Allow(_ context.Context, userID int64, topic string, ttl time.Duration) (bool, error) {
	f.calls = append(f.calls, throttleCall{UserID: userID, Topic: topic, TTL: ttl})
	if f.fail {
		return false, errors.New("redis down")
	}
	if v, ok := f.perUser[userID]; ok {
		return v, nil
	}
	return f.allow, nil
}

// fakeSender records every SendDigest call.
type fakeSender struct {
	sends []sendCall
	err   error
}

type sendCall struct {
	TGUserID int64
	Text     string
}

func (f *fakeSender) SendDigest(_ context.Context, tg int64, text string) error {
	f.sends = append(f.sends, sendCall{TGUserID: tg, Text: text})
	return f.err
}

// fakeTopics records every MatchesAny call and returns a fixed verdict.
type fakeTopics struct {
	match bool
	err   error
	calls []topicsCall
}

type topicsCall struct {
	UserID, ClusterID int64
}

func (f *fakeTopics) MatchesAny(_ context.Context, userID, clusterID int64) (bool, error) {
	f.calls = append(f.calls, topicsCall{UserID: userID, ClusterID: clusterID})
	return f.match, f.err
}

const validPayload = `{"cluster_id":42}`

func TestHandle_FreeNotEnumerated(t *testing.T) {
	// ListEligible returns ONLY Pro users (mocking the SQL join), so the
	// worker never asks the throttle for a Free user. We assert the only
	// throttle call is for the Pro user.
	users := &fakeUsers{list: []EligibleUser{{UserID: 1, TGUserID: 10, AlertThreshold: 50, ThrottleMin: 30}}}
	thr := &fakeThrottle{allow: true}
	send := &fakeSender{}
	cl := &fakeClusters{c: Cluster{Headline: "h", Topics: []string{"war"}, Score: 0.9, Coverage: 5}}
	w := NewWorker(Deps{Users: users, Clusters: cl, Throttle: thr, Sender: send})

	require.NoError(t, w.Handle(context.Background(), []byte(validPayload)))
	require.Len(t, thr.calls, 1)
	require.Equal(t, int64(1), thr.calls[0].UserID)
	require.Len(t, send.sends, 1)
}

func TestHandle_BelowThreshold_NotSent(t *testing.T) {
	users := &fakeUsers{list: []EligibleUser{{UserID: 1, TGUserID: 10, AlertThreshold: 85, ThrottleMin: 30}}}
	thr := &fakeThrottle{allow: true}
	send := &fakeSender{}
	// Score 50% < threshold 85 → skip without touching the throttle.
	cl := &fakeClusters{c: Cluster{Score: 0.5, Topics: []string{"war"}}}
	w := NewWorker(Deps{Users: users, Clusters: cl, Throttle: thr, Sender: send})

	require.NoError(t, w.Handle(context.Background(), []byte(validPayload)))
	require.Empty(t, thr.calls, "throttle must not be consulted below threshold")
	require.Empty(t, send.sends)
}

func TestHandle_ThrottleHit_NotSent(t *testing.T) {
	users := &fakeUsers{list: []EligibleUser{{UserID: 1, TGUserID: 10, AlertThreshold: 50, ThrottleMin: 30}}}
	thr := &fakeThrottle{allow: false}
	send := &fakeSender{}
	cl := &fakeClusters{c: Cluster{Score: 0.9, Topics: []string{"war"}}}
	w := NewWorker(Deps{Users: users, Clusters: cl, Throttle: thr, Sender: send})

	require.NoError(t, w.Handle(context.Background(), []byte(validPayload)))
	require.Len(t, thr.calls, 1)
	require.Empty(t, send.sends, "throttle deny must skip the send")
}

func TestHandle_HappyPath_Sent(t *testing.T) {
	users := &fakeUsers{list: []EligibleUser{{UserID: 1, TGUserID: 555, AlertThreshold: 50, ThrottleMin: 30}}}
	thr := &fakeThrottle{allow: true}
	send := &fakeSender{}
	cl := &fakeClusters{c: Cluster{
		Headline: "ЧП на заводе",
		Summary:  "Подробности уточняются.",
		Coverage: 7,
		Topics:   []string{"war"},
		Score:    0.92,
	}}
	w := NewWorker(Deps{Users: users, Clusters: cl, Throttle: thr, Sender: send})

	require.NoError(t, w.Handle(context.Background(), []byte(validPayload)))
	require.Len(t, send.sends, 1)
	require.Equal(t, int64(555), send.sends[0].TGUserID)
	require.Contains(t, send.sends[0].Text, "ЧП на заводе")
	require.Contains(t, send.sends[0].Text, "92/50") // score / threshold
	require.Contains(t, send.sends[0].Text, "7")     // coverage
	require.Equal(t, "war", thr.calls[0].Topic)
	require.Equal(t, 30*time.Minute, thr.calls[0].TTL)
}

func TestHandle_BurstyTraffic_OnlyOneSendPerWindow(t *testing.T) {
	users := &fakeUsers{list: []EligibleUser{{UserID: 1, TGUserID: 10, AlertThreshold: 50, ThrottleMin: 30}}}
	// First Allow returns true, subsequent ones false — simulates SETNX
	// over a TTL longer than the test runs.
	calls := 0
	thr := stubThrottle{fn: func() (bool, error) {
		calls++
		return calls == 1, nil
	}}
	send := &fakeSender{}
	cl := &fakeClusters{c: Cluster{Score: 0.9, Topics: []string{"war"}}}
	w := NewWorker(Deps{Users: users, Clusters: cl, Throttle: thr, Sender: send})

	for i := 0; i < 5; i++ {
		require.NoError(t, w.Handle(context.Background(), []byte(validPayload)))
	}
	require.Len(t, send.sends, 1, "throttle must collapse the burst to one push")
}

func TestHandle_TopicsMismatch_NotSent(t *testing.T) {
	users := &fakeUsers{list: []EligibleUser{{UserID: 1, TGUserID: 10, AlertThreshold: 50, ThrottleMin: 30}}}
	thr := &fakeThrottle{allow: true}
	send := &fakeSender{}
	cl := &fakeClusters{c: Cluster{Score: 0.9, Topics: []string{"war"}}}
	tpc := &fakeTopics{match: false}
	w := NewWorker(Deps{Users: users, Clusters: cl, Throttle: thr, Sender: send, Topics: tpc})

	require.NoError(t, w.Handle(context.Background(), []byte(validPayload)))
	require.Len(t, tpc.calls, 1)
	require.Empty(t, thr.calls, "throttle must not be consulted when topics gate denies")
	require.Empty(t, send.sends)
}

func TestHandle_TopicsErrorFailsOpen(t *testing.T) {
	users := &fakeUsers{list: []EligibleUser{{UserID: 1, TGUserID: 10, AlertThreshold: 50, ThrottleMin: 30}}}
	thr := &fakeThrottle{allow: true}
	send := &fakeSender{}
	cl := &fakeClusters{c: Cluster{Score: 0.9, Topics: []string{"war"}}}
	tpc := &fakeTopics{err: errors.New("vector store down")}
	w := NewWorker(Deps{Users: users, Clusters: cl, Throttle: thr, Sender: send, Topics: tpc})

	require.NoError(t, w.Handle(context.Background(), []byte(validPayload)))
	require.Len(t, send.sends, 1, "TopicMatcher errors must not silence the alert")
}

func TestHandle_PassThroughTopicsDefault(t *testing.T) {
	// Default TopicMatcher when none is wired = PassThroughTopics — every
	// cluster passes through to the throttle stage.
	users := &fakeUsers{list: []EligibleUser{{UserID: 1, TGUserID: 10, AlertThreshold: 50, ThrottleMin: 30}}}
	thr := &fakeThrottle{allow: true}
	send := &fakeSender{}
	cl := &fakeClusters{c: Cluster{Score: 0.9, Topics: []string{"war"}}}
	w := NewWorker(Deps{Users: users, Clusters: cl, Throttle: thr, Sender: send /* Topics nil */})

	require.NoError(t, w.Handle(context.Background(), []byte(validPayload)))
	require.Len(t, send.sends, 1)
}

func TestHandle_TopicKeyFallsBackToOther(t *testing.T) {
	users := &fakeUsers{list: []EligibleUser{{UserID: 1, TGUserID: 10, AlertThreshold: 50, ThrottleMin: 30}}}
	thr := &fakeThrottle{allow: true}
	send := &fakeSender{}
	// No topics at all → topic key must default to "other".
	cl := &fakeClusters{c: Cluster{Score: 0.9}}
	w := NewWorker(Deps{Users: users, Clusters: cl, Throttle: thr, Sender: send})

	require.NoError(t, w.Handle(context.Background(), []byte(validPayload)))
	require.Equal(t, "other", thr.calls[0].Topic)
}

func TestHandle_RejectsZeroClusterID(t *testing.T) {
	w := NewWorker(Deps{Users: &fakeUsers{}, Clusters: &fakeClusters{}, Throttle: &fakeThrottle{}, Sender: &fakeSender{}})
	err := w.Handle(context.Background(), []byte(`{"cluster_id":0}`))
	require.Error(t, err)
}

func TestHandle_RejectsMalformedPayload(t *testing.T) {
	w := NewWorker(Deps{Users: &fakeUsers{}, Clusters: &fakeClusters{}, Throttle: &fakeThrottle{}, Sender: &fakeSender{}})
	err := w.Handle(context.Background(), []byte(`{not json`))
	require.Error(t, err)
}

func TestHandle_SendErrorLoggedNotPropagated(t *testing.T) {
	users := &fakeUsers{list: []EligibleUser{
		{UserID: 1, TGUserID: 10, AlertThreshold: 50, ThrottleMin: 30},
		{UserID: 2, TGUserID: 20, AlertThreshold: 50, ThrottleMin: 30},
	}}
	thr := &fakeThrottle{allow: true}
	send := &fakeSender{err: errors.New("blocked")}
	cl := &fakeClusters{c: Cluster{Score: 0.9, Topics: []string{"war"}}}
	w := NewWorker(Deps{Users: users, Clusters: cl, Throttle: thr, Sender: send})

	// Two users; both Sender calls fail but Handle still returns nil and
	// runs through every user (does not short-circuit on first error).
	require.NoError(t, w.Handle(context.Background(), []byte(validPayload)))
	require.Len(t, send.sends, 2)
}

func TestHandle_ThrottleErrorSkipsUserContinuesOthers(t *testing.T) {
	users := &fakeUsers{list: []EligibleUser{
		{UserID: 1, TGUserID: 10, AlertThreshold: 50, ThrottleMin: 30},
		{UserID: 2, TGUserID: 20, AlertThreshold: 50, ThrottleMin: 30},
	}}
	send := &fakeSender{}
	cl := &fakeClusters{c: Cluster{Score: 0.9, Topics: []string{"war"}}}
	// First user → throttle errors out; second user → throttle allows.
	thr := stubThrottle{fn: func() (bool, error) {
		// Track per-call so first errors, second allows.
		return false, nil
	}}
	thr.fn = func() func() (bool, error) {
		n := 0
		return func() (bool, error) {
			n++
			if n == 1 {
				return false, errors.New("redis hiccup")
			}
			return true, nil
		}
	}()
	w := NewWorker(Deps{Users: users, Clusters: cl, Throttle: thr, Sender: send})

	require.NoError(t, w.Handle(context.Background(), []byte(validPayload)))
	require.Len(t, send.sends, 1)
	require.Equal(t, int64(20), send.sends[0].TGUserID)
}

// stubThrottle is a function-based Throttler used by tests that need
// per-call dynamic behaviour (burst test, mid-loop error). Keeps test
// setup terse compared to a hand-rolled struct + counter pair.
type stubThrottle struct {
	fn func() (bool, error)
}

func (s stubThrottle) Allow(_ context.Context, _ int64, _ string, _ time.Duration) (bool, error) {
	return s.fn()
}

func TestPassThroughTopicsAlwaysMatches(t *testing.T) {
	ok, err := PassThroughTopics{}.MatchesAny(context.Background(), 1, 2)
	require.NoError(t, err)
	require.True(t, ok)
}
