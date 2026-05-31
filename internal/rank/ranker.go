// Package rank computes a single importance score per cluster from coverage,
// source authority and classifier-derived severity, then persists it back to
// the clusters table. The score is what the digest assembler ultimately sorts
// on.
package rank

import (
	"context"
	"fmt"
	"math"
)

// Snapshot is the read model the ranker needs from storage: the aggregate
// signals that feed into the score formula.
type Snapshot struct {
	Coverage     int
	Severity     int
	MaxAuthority int
}

// Weights tune the relative contribution of each signal. Sum is not required
// to be 1 — the formula multiplies each weight by its raw signal directly.
type Weights struct {
	Coverage  float64
	Authority float64
	Severity  float64
}

// DefaultWeights is the Phase-1 setting. Tweak via golden-dataset evaluation
// before promoting a change.
var DefaultWeights = Weights{Coverage: 0.4, Authority: 0.3, Severity: 0.3}

// ClustersRanker is the slice of the clusters repository the ranker needs.
type ClustersRanker interface {
	Snapshot(ctx context.Context, clusterID int64) (Snapshot, error)
	SetScore(ctx context.Context, clusterID int64, score float32) error
}

// RankerDeps groups the Ranker's collaborators.
type RankerDeps struct {
	Clusters ClustersRanker
	Weights  Weights
}

// Ranker computes and persists the score for a single cluster.
type Ranker struct {
	d RankerDeps
}

// NewRanker constructs a Ranker bound to d. Zero Weights fall back to
// DefaultWeights so callers can omit them in tests.
func NewRanker(d RankerDeps) *Ranker {
	if d.Weights == (Weights{}) {
		d.Weights = DefaultWeights
	}
	return &Ranker{d: d}
}

// Score loads the snapshot for clusterID, evaluates the formula and writes
// the result back via SetScore.
//
// Formula:
//
//	score = w_cov * log(coverage+1) + w_auth * max_authority + w_sev * severity/100
//
// The log dampens runaway coverage (a story in 50 channels is not 5x more
// important than a story in 10). Authority is on the same 0..100 scale as
// severity but contributes linearly so domain-trusted sources outweigh
// random reposters.
func (r *Ranker) Score(ctx context.Context, clusterID int64) error {
	s, err := r.d.Clusters.Snapshot(ctx, clusterID)
	if err != nil {
		return fmt.Errorf("snapshot: %w", err)
	}
	score := r.d.Weights.Coverage*math.Log(float64(s.Coverage+1)) +
		r.d.Weights.Authority*float64(s.MaxAuthority) +
		r.d.Weights.Severity*float64(s.Severity)/100
	if err := r.d.Clusters.SetScore(ctx, clusterID, float32(score)); err != nil {
		return fmt.Errorf("set score: %w", err)
	}
	return nil
}
