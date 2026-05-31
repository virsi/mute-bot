package dedup

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/virsi/mute-bot/internal/normalize"
)

// MatcherIface is the slice of *Matcher the worker depends on. Narrow on
// purpose so the worker can be unit-tested without instantiating the full
// orchestrator graph.
type MatcherIface interface {
	Match(ctx context.Context, in MatchInput) error
}

// WorkerDeps groups the dedup worker's collaborators.
type WorkerDeps struct {
	Matcher MatcherIface
}

// Worker consumes NormalizedPostEvent messages off the ingest.normalized
// subject and delegates each one to the Matcher. It is intentionally a thin
// shell — all decisions live in the matcher.
type Worker struct {
	d WorkerDeps
}

// NewWorker constructs a Worker bound to d.
func NewWorker(d WorkerDeps) *Worker { return &Worker{d: d} }

// Handle is the JetStream message callback. The dedup pipeline is
// idempotent: when a post is redelivered, MinHash/embedding will rediscover
// the same cluster, AttachCluster is an UPDATE on a known row, and
// IncrementCoverage will overcount by at most the number of redeliveries —
// acceptable for Phase 1 in exchange for keeping the handler simple.
func (w *Worker) Handle(ctx context.Context, data []byte) error {
	var evt normalize.NormalizedPostEvent
	if err := json.Unmarshal(data, &evt); err != nil {
		return fmt.Errorf("unmarshal normalized event: %w", err)
	}
	return w.d.Matcher.Match(ctx, MatchInput{
		PostID:    evt.PostID,
		TextClean: evt.TextClean,
		Hash:      evt.TextHash,
	})
}
