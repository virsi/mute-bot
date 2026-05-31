package rank

import (
	"context"
	"encoding/json"
	"fmt"
)

// Worker is the JetStream subscriber side of the ranker. It listens on
// cluster.scored events (published by the classifier worker after a
// successful classification) and delegates to *Ranker.
type Worker struct {
	r *Ranker
}

// NewWorker constructs a Worker that drives r.
func NewWorker(r *Ranker) *Worker { return &Worker{r: r} }

// Handle is the JetStream message callback. The payload shape mirrors the
// one the classifier worker publishes: a single cluster_id key.
func (w *Worker) Handle(ctx context.Context, data []byte) error {
	var evt struct {
		ClusterID int64 `json:"cluster_id"`
	}
	if err := json.Unmarshal(data, &evt); err != nil {
		return fmt.Errorf("unmarshal cluster.scored: %w", err)
	}
	if evt.ClusterID == 0 {
		return fmt.Errorf("cluster.scored: zero cluster_id")
	}
	return w.r.Score(ctx, evt.ClusterID)
}
