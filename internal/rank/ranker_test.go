package rank

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

type fakeClusters struct {
	snap    Snapshot
	snapErr error
	setErr  error

	saved   float32
	savedID int64
	calls   int
}

func (f *fakeClusters) Snapshot(_ context.Context, _ int64) (Snapshot, error) {
	if f.snapErr != nil {
		return Snapshot{}, f.snapErr
	}
	return f.snap, nil
}

func (f *fakeClusters) SetScore(_ context.Context, id int64, s float32) error {
	f.calls++
	if f.setErr != nil {
		return f.setErr
	}
	f.savedID = id
	f.saved = s
	return nil
}

func TestRanker_Computes_WithExplicitWeights(t *testing.T) {
	c := &fakeClusters{snap: Snapshot{Coverage: 5, MaxAuthority: 80, Severity: 70}}
	r := NewRanker(RankerDeps{
		Clusters: c,
		Weights:  Weights{Coverage: 0.4, Authority: 0.3, Severity: 0.3},
	})

	require.NoError(t, r.Score(context.Background(), 99))
	expected := float32(0.4*math.Log(6) + 0.3*80 + 0.3*70/100)
	require.InDelta(t, expected, c.saved, 0.001)
	require.Equal(t, int64(99), c.savedID)
}

func TestRanker_UsesDefaultWeightsWhenZero(t *testing.T) {
	c := &fakeClusters{snap: Snapshot{Coverage: 0, MaxAuthority: 0, Severity: 0}}
	r := NewRanker(RankerDeps{Clusters: c})

	require.NoError(t, r.Score(context.Background(), 1))
	// log(1)=0; everything else zero → score 0.
	require.InDelta(t, 0.0, float64(c.saved), 0.001)
}

func TestRanker_HighCoverageIsLogDampened(t *testing.T) {
	c10 := &fakeClusters{snap: Snapshot{Coverage: 10, MaxAuthority: 0, Severity: 0}}
	c100 := &fakeClusters{snap: Snapshot{Coverage: 100, MaxAuthority: 0, Severity: 0}}
	w := Weights{Coverage: 1, Authority: 0, Severity: 0}

	require.NoError(t, NewRanker(RankerDeps{Clusters: c10, Weights: w}).Score(context.Background(), 1))
	require.NoError(t, NewRanker(RankerDeps{Clusters: c100, Weights: w}).Score(context.Background(), 1))

	// 100/10 = 10, but log(101)/log(11) ≈ 1.92, far short of 10×.
	ratio := float64(c100.saved) / float64(c10.saved)
	require.Less(t, ratio, 2.5)
	require.Greater(t, ratio, 1.5)
}

func TestRanker_ReturnsErrorOnSnapshotFailure(t *testing.T) {
	c := &fakeClusters{snapErr: errors.New("db gone")}
	r := NewRanker(RankerDeps{Clusters: c})
	err := r.Score(context.Background(), 1)
	require.Error(t, err)
	require.ErrorContains(t, err, "snapshot")
	require.Equal(t, 0, c.calls)
}

func TestRanker_ReturnsErrorOnSetScoreFailure(t *testing.T) {
	c := &fakeClusters{snap: Snapshot{Coverage: 1}, setErr: errors.New("write failed")}
	r := NewRanker(RankerDeps{Clusters: c})
	err := r.Score(context.Background(), 1)
	require.Error(t, err)
	require.ErrorContains(t, err, "set score")
}
