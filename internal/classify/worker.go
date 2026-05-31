package classify

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/virsi/mute-bot/internal/queue"
)

// defaultDebounce is the wait window applied to cluster.updated events. A
// cluster keeps growing as the dedup pipeline attaches more posts; debouncing
// lets the classifier see a fuller set of texts in a single call.
const defaultDebounce = 60 * time.Second

// PostsReader is the slice of the posts repository the worker needs. Returns
// the cleaned texts attached to a cluster and the dominant language.
type PostsReader interface {
	ListTextsByCluster(ctx context.Context, clusterID int64) ([]string, string, error)
}

// MetaUpdate is the field set the classifier hands to the clusters repository.
// Declared as a named type (rather than the anonymous struct used elsewhere in
// the plan) so the ClustersUpdater interface stays readable and adapter wiring
// in cmd/processor can name the type explicitly.
type MetaUpdate struct {
	Headline string
	Summary  string
	Topics   []string
	Severity int
}

// ClustersUpdater is the slice of the clusters repository the worker needs.
// Wired in cmd/processor via ClustersUpdaterFunc to bridge the storage layer.
type ClustersUpdater interface {
	UpdateMeta(ctx context.Context, id int64, m MetaUpdate) error
}

// ClustersUpdaterFunc adapts an ordinary function to ClustersUpdater so the
// wiring layer can supply an inline closure that translates MetaUpdate to the
// storage-side ClusterMeta type.
type ClustersUpdaterFunc func(ctx context.Context, id int64, m MetaUpdate) error

// UpdateMeta calls f.
func (f ClustersUpdaterFunc) UpdateMeta(ctx context.Context, id int64, m MetaUpdate) error {
	return f(ctx, id, m)
}

// Compile-time guarantee: the func adapter satisfies the interface.
var _ ClustersUpdater = ClustersUpdaterFunc(nil)

// ClassifierIface is the slice of *Classifier the worker depends on. Keeps
// the worker unit-testable without an LLM client.
type ClassifierIface interface {
	Classify(ctx context.Context, posts []string, lang string) (Result, error)
}

// Publisher is the slice of queue.Publisher the worker uses to emit
// cluster.scored once a classification is persisted.
type Publisher interface {
	Publish(ctx context.Context, subject string, payload any) error
}

// WorkerDeps groups the worker's collaborators.
type WorkerDeps struct {
	Classifier ClassifierIface
	Posts      PostsReader
	Clusters   ClustersUpdater
	Publisher  Publisher
	// Debounce is the wait window between the first cluster.updated event
	// for a cluster and the actual classification call. Zero falls back to
	// defaultDebounce.
	Debounce time.Duration
}

// Worker subscribes to cluster.updated, debounces per-cluster, then classifies
// the cluster's posts and publishes cluster.scored. The debounce is necessary
// because dedup can emit several cluster.updated events per second while a
// breaking story propagates across channels.
type Worker struct {
	d        WorkerDeps
	mu       sync.Mutex
	pending  map[int64]time.Time
	debounce time.Duration
}

// NewWorker constructs a Worker bound to d. The debounce defaults to 60s when
// d.Debounce is zero.
func NewWorker(d WorkerDeps) *Worker {
	deb := d.Debounce
	if deb <= 0 {
		deb = defaultDebounce
	}
	return &Worker{
		d:        d,
		pending:  make(map[int64]time.Time),
		debounce: deb,
	}
}

// Handle is the JetStream message callback. It records (or refreshes) the
// debounce deadline for the cluster id and returns immediately so JetStream
// can ack the message. Actual classification happens in runDebouncer.
func (w *Worker) Handle(_ context.Context, data []byte) error {
	var evt struct {
		ClusterID int64 `json:"cluster_id"`
	}
	if err := json.Unmarshal(data, &evt); err != nil {
		return fmt.Errorf("unmarshal cluster.updated: %w", err)
	}
	if evt.ClusterID == 0 {
		return fmt.Errorf("cluster.updated: zero cluster_id")
	}
	w.mu.Lock()
	w.pending[evt.ClusterID] = time.Now().Add(w.debounce)
	w.mu.Unlock()
	return nil
}

// Run starts the debouncer loop. It blocks until ctx is done.
func (w *Worker) Run(ctx context.Context) { w.runDebouncer(ctx) }

// runDebouncer polls the pending map at debounce/2 frequency and processes
// clusters whose deadline has elapsed. Errors are swallowed: a missed
// classification will be retried by the next cluster.updated event.
func (w *Worker) runDebouncer(ctx context.Context) {
	interval := w.debounce / 2
	if interval <= 0 {
		interval = time.Millisecond
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			ready := w.drainReady(now)
			for _, id := range ready {
				if err := w.process(ctx, id); err != nil {
					// Log-only: the next cluster.updated event will re-push.
					continue
				}
			}
		}
	}
}

// drainReady removes and returns all cluster ids whose debounce deadline has
// elapsed. Held lock is released before processing to keep Handle cheap.
func (w *Worker) drainReady(now time.Time) []int64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	ready := make([]int64, 0, len(w.pending))
	for id, due := range w.pending {
		if !now.Before(due) {
			ready = append(ready, id)
			delete(w.pending, id)
		}
	}
	return ready
}

// process classifies a single cluster and persists the result. It returns
// nil for clusters that have no posts attached (a redelivery race; another
// cluster.updated event will re-trigger).
func (w *Worker) process(ctx context.Context, clusterID int64) error {
	texts, lang, err := w.d.Posts.ListTextsByCluster(ctx, clusterID)
	if err != nil {
		return fmt.Errorf("list texts: %w", err)
	}
	if len(texts) == 0 {
		return nil
	}
	res, err := w.d.Classifier.Classify(ctx, texts, lang)
	if err != nil {
		return fmt.Errorf("classify: %w", err)
	}
	if err := w.d.Clusters.UpdateMeta(ctx, clusterID, MetaUpdate{
		Headline: res.Headline,
		Summary:  res.Summary,
		Topics:   res.Topics,
		Severity: res.Severity,
	}); err != nil {
		return fmt.Errorf("update meta: %w", err)
	}
	if err := w.d.Publisher.Publish(ctx, queue.SubjectClusterScored, map[string]any{
		"cluster_id": clusterID,
	}); err != nil {
		return fmt.Errorf("publish cluster.scored: %w", err)
	}
	return nil
}
