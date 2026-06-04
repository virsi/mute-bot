package dedup

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	redisstore "github.com/virsi/mute-bot/internal/storage/redis"
)

// BorderlineDrainer is the Redis list contract the reconciler needs.
type BorderlineDrainer interface {
	Drain(ctx context.Context, limit int) ([]redisstore.BorderlinePair, error)
}

// PostsForJudge is the storage surface the reconciler needs: load post text
// for two ids, look up the cluster a post belongs to, and (for non-clustered
// posts) attach a new one.
type PostsForJudge interface {
	GetText(ctx context.Context, postID int64) (string, error)
	GetClusterID(ctx context.Context, postID int64) (int64, error)
}

// ClustersForJudge knows how to merge cluster from into cluster into (move
// all posts attached to from onto into and mark from.status='merged').
type ClustersForJudge interface {
	Merge(ctx context.Context, into, from int64) error
}

// JudgeDecider is the LLMJudge surface used here.
type JudgeDecider interface {
	Decide(ctx context.Context, a, b string) (bool, float64, error)
}

// ReconcilerDeps configures Reconciler.
type ReconcilerDeps struct {
	Queue         BorderlineDrainer
	Posts         PostsForJudge
	Clusters      ClustersForJudge
	Judge         JudgeDecider
	Interval      time.Duration // default 5 min
	BatchSize     int           // default 50
	MinConfidence float64       // default 0.8
	Logger        *slog.Logger
}

// Reconciler drains the dedup:borderline list every Interval, asks LLMJudge
// per pair, and merges clusters when same=true && confidence >= MinConfidence.
// It is the slow-path counterpart to Matcher.Match — the matcher emits the
// pair on the hot path, the reconciler resolves it asynchronously.
type Reconciler struct {
	d ReconcilerDeps
	// stepMu serialises Step. Run() guarantees only one in-flight Step at a
	// time (single-goroutine ticker drops late ticks), but tests and manual
	// triggers can race; the mutex protects Drain+Merge from concurrent
	// callers in those cases.
	stepMu sync.Mutex
}

// NewReconciler constructs a Reconciler with sensible defaults applied.
func NewReconciler(d ReconcilerDeps) *Reconciler {
	if d.Interval == 0 {
		d.Interval = 5 * time.Minute
	}
	if d.BatchSize == 0 {
		d.BatchSize = 50
	}
	if d.MinConfidence == 0 {
		d.MinConfidence = 0.8
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	return &Reconciler{d: d}
}

// stepLocked is the body of Step protected by stepMu so concurrent callers
// can't double-drain the same borderline pair from Redis.
func (r *Reconciler) stepLocked(ctx context.Context) error {
	pairs, err := r.d.Queue.Drain(ctx, r.d.BatchSize)
	if err != nil {
		return fmt.Errorf("drain: %w", err)
	}
	for _, p := range pairs {
		if err := r.judgeOne(ctx, p); err != nil {
			r.d.Logger.WarnContext(ctx, "judge pair",
				slog.Int64("post", p.PostID),
				slog.Int64("cand", p.CandidateID),
				slog.Any("err", err))
		}
	}
	return nil
}

// Run blocks until ctx is cancelled, calling Step on every tick. A failing
// step is logged and the tick continues — one bad LLM call must not stop
// the reconciler from draining future pairs.
func (r *Reconciler) Run(ctx context.Context) error {
	t := time.NewTicker(r.d.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			if err := r.Step(ctx); err != nil {
				r.d.Logger.WarnContext(ctx, "borderline step", slog.Any("err", err))
			}
		}
	}
}

// Step drains BatchSize pairs and acts on each. A judgement failure on one
// pair is logged and skipped; subsequent pairs are still processed.
func (r *Reconciler) Step(ctx context.Context) error {
	r.stepMu.Lock()
	defer r.stepMu.Unlock()
	return r.stepLocked(ctx)
}

// judgeOne loads both texts, asks the judge, and merges the two clusters if
// the judge is confident enough. Merges canonicalise on the older cluster id
// (smaller value wins) so consumers see a stable identifier over time.
func (r *Reconciler) judgeOne(ctx context.Context, p redisstore.BorderlinePair) error {
	a, err := r.d.Posts.GetText(ctx, p.PostID)
	if err != nil {
		return err
	}
	b, err := r.d.Posts.GetText(ctx, p.CandidateID)
	if err != nil {
		return err
	}
	same, conf, err := r.d.Judge.Decide(ctx, a, b)
	if err != nil {
		return err
	}
	if !same || conf < r.d.MinConfidence {
		return nil
	}
	cA, err := r.d.Posts.GetClusterID(ctx, p.PostID)
	if err != nil {
		return err
	}
	cB, err := r.d.Posts.GetClusterID(ctx, p.CandidateID)
	if err != nil {
		return err
	}
	if cA == cB || cA == 0 || cB == 0 {
		return nil
	}
	into, from := cA, cB
	if cB < cA {
		into, from = cB, cA
	}
	return r.d.Clusters.Merge(ctx, into, from)
}
