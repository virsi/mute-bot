package dedup

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	redisstore "github.com/virsi/mute-bot/internal/storage/redis"
)

type fakeBorderlineDrainer struct {
	pairs []redisstore.BorderlinePair
	err   error
}

func (f *fakeBorderlineDrainer) Drain(_ context.Context, limit int) ([]redisstore.BorderlinePair, error) {
	if f.err != nil {
		return nil, f.err
	}
	if limit >= len(f.pairs) {
		out := f.pairs
		f.pairs = nil
		return out, nil
	}
	out := f.pairs[:limit]
	f.pairs = f.pairs[limit:]
	return out, nil
}

type fakePostsForJudge struct {
	texts    map[int64]string
	clusters map[int64]int64
	textErr  map[int64]error
}

func (f *fakePostsForJudge) GetText(_ context.Context, id int64) (string, error) {
	if err, ok := f.textErr[id]; ok {
		return "", err
	}
	return f.texts[id], nil
}

func (f *fakePostsForJudge) GetClusterID(_ context.Context, id int64) (int64, error) {
	return f.clusters[id], nil
}

type fakeClustersForJudge struct {
	calls [][2]int64
}

func (f *fakeClustersForJudge) Merge(_ context.Context, into, from int64) error {
	f.calls = append(f.calls, [2]int64{into, from})
	return nil
}

type fakeJudge struct {
	same bool
	conf float64
	err  error
}

func (f *fakeJudge) Decide(_ context.Context, _, _ string) (bool, float64, error) {
	return f.same, f.conf, f.err
}

func newRec(d ReconcilerDeps) *Reconciler {
	return NewReconciler(d)
}

func TestReconciler_HighConfMerges(t *testing.T) {
	q := &fakeBorderlineDrainer{pairs: []redisstore.BorderlinePair{{PostID: 7, CandidateID: 3, Distance: 0.18}}}
	posts := &fakePostsForJudge{
		texts:    map[int64]string{7: "Post seven", 3: "Post three"},
		clusters: map[int64]int64{7: 200, 3: 100},
	}
	cl := &fakeClustersForJudge{}
	r := newRec(ReconcilerDeps{Queue: q, Posts: posts, Clusters: cl, Judge: &fakeJudge{same: true, conf: 0.9}})

	require.NoError(t, r.Step(context.Background()))
	require.Len(t, cl.calls, 1)
	// older cluster id (100) wins as "into", newer (200) gets merged away.
	require.Equal(t, [2]int64{100, 200}, cl.calls[0])
}

func TestReconciler_NotSame_NoMerge(t *testing.T) {
	q := &fakeBorderlineDrainer{pairs: []redisstore.BorderlinePair{{PostID: 7, CandidateID: 3}}}
	posts := &fakePostsForJudge{
		texts:    map[int64]string{7: "a", 3: "b"},
		clusters: map[int64]int64{7: 1, 3: 2},
	}
	cl := &fakeClustersForJudge{}
	r := newRec(ReconcilerDeps{Queue: q, Posts: posts, Clusters: cl, Judge: &fakeJudge{same: false, conf: 0.95}})

	require.NoError(t, r.Step(context.Background()))
	require.Empty(t, cl.calls)
}

func TestReconciler_LowConf_NoMerge(t *testing.T) {
	q := &fakeBorderlineDrainer{pairs: []redisstore.BorderlinePair{{PostID: 7, CandidateID: 3}}}
	posts := &fakePostsForJudge{
		texts:    map[int64]string{7: "a", 3: "b"},
		clusters: map[int64]int64{7: 1, 3: 2},
	}
	cl := &fakeClustersForJudge{}
	r := newRec(ReconcilerDeps{Queue: q, Posts: posts, Clusters: cl, Judge: &fakeJudge{same: true, conf: 0.7}})

	require.NoError(t, r.Step(context.Background()))
	require.Empty(t, cl.calls)
}

func TestReconciler_OneClusterZero_NoMerge(t *testing.T) {
	q := &fakeBorderlineDrainer{pairs: []redisstore.BorderlinePair{{PostID: 7, CandidateID: 3}}}
	posts := &fakePostsForJudge{
		texts:    map[int64]string{7: "a", 3: "b"},
		clusters: map[int64]int64{7: 0, 3: 5},
	}
	cl := &fakeClustersForJudge{}
	r := newRec(ReconcilerDeps{Queue: q, Posts: posts, Clusters: cl, Judge: &fakeJudge{same: true, conf: 0.99}})

	require.NoError(t, r.Step(context.Background()))
	require.Empty(t, cl.calls)
}

func TestReconciler_JudgeError_StillProcessesNext(t *testing.T) {
	q := &fakeBorderlineDrainer{pairs: []redisstore.BorderlinePair{
		{PostID: 7, CandidateID: 3},
		{PostID: 11, CandidateID: 13},
	}}
	posts := &fakePostsForJudge{
		texts:    map[int64]string{7: "a", 3: "b", 11: "c", 13: "d"},
		clusters: map[int64]int64{7: 1, 3: 2, 11: 10, 13: 20},
		textErr:  map[int64]error{7: errors.New("oops")}, // first pair fails, second must run
	}
	cl := &fakeClustersForJudge{}
	r := newRec(ReconcilerDeps{Queue: q, Posts: posts, Clusters: cl, Judge: &fakeJudge{same: true, conf: 0.99}})

	require.NoError(t, r.Step(context.Background()))
	require.Len(t, cl.calls, 1)
	require.Equal(t, [2]int64{10, 20}, cl.calls[0])
}

func TestReconciler_SameCluster_NoMerge(t *testing.T) {
	q := &fakeBorderlineDrainer{pairs: []redisstore.BorderlinePair{{PostID: 7, CandidateID: 3}}}
	posts := &fakePostsForJudge{
		texts:    map[int64]string{7: "a", 3: "b"},
		clusters: map[int64]int64{7: 42, 3: 42},
	}
	cl := &fakeClustersForJudge{}
	r := newRec(ReconcilerDeps{Queue: q, Posts: posts, Clusters: cl, Judge: &fakeJudge{same: true, conf: 0.99}})

	require.NoError(t, r.Step(context.Background()))
	require.Empty(t, cl.calls)
}
