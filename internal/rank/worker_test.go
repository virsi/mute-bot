package rank

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWorker_DelegatesToRanker(t *testing.T) {
	c := &fakeClusters{snap: Snapshot{Coverage: 2, MaxAuthority: 50, Severity: 40}}
	w := NewWorker(NewRanker(RankerDeps{Clusters: c}))

	evt, _ := json.Marshal(map[string]any{"cluster_id": 7})
	require.NoError(t, w.Handle(context.Background(), evt))
	require.Equal(t, int64(7), c.savedID)
	require.NotZero(t, c.saved)
}

func TestWorker_RejectsInvalidJSON(t *testing.T) {
	w := NewWorker(NewRanker(RankerDeps{Clusters: &fakeClusters{}}))
	require.Error(t, w.Handle(context.Background(), []byte("not json")))
}

func TestWorker_RejectsZeroClusterID(t *testing.T) {
	w := NewWorker(NewRanker(RankerDeps{Clusters: &fakeClusters{}}))
	evt, _ := json.Marshal(map[string]any{"cluster_id": 0})
	require.Error(t, w.Handle(context.Background(), evt))
}
