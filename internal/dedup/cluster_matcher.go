package dedup

import (
	"context"
	"fmt"
	"time"

	"github.com/pgvector/pgvector-go"

	"github.com/virsi/mute-bot/internal/queue"
	"github.com/virsi/mute-bot/internal/storage/postgres"
	redisstore "github.com/virsi/mute-bot/internal/storage/redis"
)

// MHIndex is the slice of the MinHash LSH index this orchestrator depends
// on — narrow on purpose so unit tests can stub it.
type MHIndex interface {
	Add(ctx context.Context, postID int64, sig []uint32) error
	Candidates(ctx context.Context, sig []uint32, limit int) ([]int64, error)
}

// EmbedderIface is the slice of the Embedder interface the matcher uses.
type EmbedderIface interface {
	EmbedOne(ctx context.Context, text string, hash [32]byte) ([]float32, error)
}

// EmbeddingsStore is the slice of EmbeddingsRepo the matcher needs: it
// persists the embedding and runs nearest-neighbor lookups.
type EmbeddingsStore interface {
	Store(ctx context.Context, postID int64, v pgvector.Vector, model string) error
	NearestNeighbors(ctx context.Context, v pgvector.Vector, p postgres.NearestParams) ([]postgres.Neighbor, error)
}

// ClustersStore is the slice of ClustersRepo the matcher uses: create new
// clusters and bump coverage on existing ones.
type ClustersStore interface {
	Create(ctx context.Context) (int64, error)
	IncrementCoverage(ctx context.Context, id int64) error
}

// PostsStore is the slice of PostsRepo the matcher uses to look up an
// existing post's cluster (candidate evaluation) and attach a cluster to
// the incoming post.
type PostsStore interface {
	GetClusterID(ctx context.Context, postID int64) (int64, error)
	AttachCluster(ctx context.Context, postID, clusterID int64) error
}

// Publisher is the slice of the queue publisher the matcher uses to
// announce cluster updates so downstream stages (classifier, ranker) wake
// up.
type Publisher interface {
	Publish(ctx context.Context, subject string, payload any) error
}

// BorderlinePusher receives (post_id, candidate_id, distance) tuples when
// the nearest neighbor's cosine distance falls between MaxDistance and
// MaxDistance + BorderlineWidth. The reconciler goroutine drains these and
// asks LLMJudge to decide whether the pair should be merged retroactively.
type BorderlinePusher interface {
	Push(ctx context.Context, p redisstore.BorderlinePair) error
}

// MatcherDeps groups the Matcher's collaborators.
type MatcherDeps struct {
	MinHash      *MinHash
	MinHashIndex MHIndex
	Embedder     EmbedderIface
	Embeddings   EmbeddingsStore
	Clusters     ClustersStore
	Posts        PostsStore
	Publisher    Publisher
	Model        string
	// NearLimit caps how many neighbors the kNN query returns.
	NearLimit int
	// MaxDistance is the cosine-distance threshold below which an existing
	// post is accepted as the same event (0.15 ≈ similarity 0.85).
	MaxDistance float32
	// BorderlineWidth is the band above MaxDistance that counts as
	// borderline. Default 0.10, so [0.15, 0.25] with default MaxDistance.
	BorderlineWidth float32
	// Borderline is an optional queue that receives (post_id, candidate_id,
	// distance) tuples for pairs in the borderline band. Nil disables the
	// signal — the matcher still works, just without LLMJudge wake-ups.
	Borderline BorderlinePusher
}

// Matcher orchestrates the dedup decision for a single normalized post:
//
//  1. compute its MinHash signature and ask the LSH index for collision
//     candidates — cheap, fully local
//  2. if any candidate already belongs to a cluster, attach to that cluster
//  3. otherwise embed the post, store the embedding, and run kNN against
//     the recent window — semantic match
//  4. if no neighbor is close enough, create a brand-new cluster
//
// In all cases the post's MinHash signature is registered in the index so
// later posts can be matched to it, and a cluster.updated event is published
// to wake downstream stages.
type Matcher struct {
	d MatcherDeps
}

// NewMatcher constructs a Matcher with sensible defaults applied to d.
func NewMatcher(d MatcherDeps) *Matcher {
	if d.MinHash == nil {
		d.MinHash = NewMinHash(MinHashConfig{NumHashes: 128, ShingleSize: 5})
	}
	if d.NearLimit == 0 {
		d.NearLimit = 5
	}
	if d.MaxDistance == 0 {
		d.MaxDistance = 0.15
	}
	if d.BorderlineWidth == 0 {
		d.BorderlineWidth = 0.10
	}
	if d.Model == "" {
		d.Model = "text-embedding-3-small"
	}
	return &Matcher{d: d}
}

// MatchInput is the per-post payload Match operates on.
type MatchInput struct {
	PostID    int64
	TextClean string
	Hash      [32]byte
}

// Match runs the full dedup pipeline for one post. It never returns nil on a
// transient downstream failure — the worker layer is responsible for
// translating errors into nack/redelivery decisions.
func (m *Matcher) Match(ctx context.Context, in MatchInput) error {
	sig := m.d.MinHash.Sign(in.TextClean)

	candidates, err := m.d.MinHashIndex.Candidates(ctx, sig, 16)
	if err != nil {
		return fmt.Errorf("minhash candidates: %w", err)
	}
	if clusterID, ok, err := m.matchByCandidates(ctx, candidates, in.PostID); err != nil {
		return err
	} else if ok {
		return m.finalize(ctx, in, sig, clusterID, "minhash")
	}

	vec, err := m.d.Embedder.EmbedOne(ctx, in.TextClean, in.Hash)
	if err != nil {
		return fmt.Errorf("embed: %w", err)
	}
	pv := pgvector.NewVector(vec)
	if err := m.d.Embeddings.Store(ctx, in.PostID, pv, m.d.Model); err != nil {
		return fmt.Errorf("store embedding: %w", err)
	}

	// Widen the kNN bound to MaxDistance + BorderlineWidth so we can
	// inspect close-but-not-close-enough neighbors. The dedup decision
	// still uses MaxDistance; anything between the two thresholds is
	// classified as borderline and forwarded to the reconciler.
	near, err := m.d.Embeddings.NearestNeighbors(ctx, pv, postgres.NearestParams{
		Limit:             m.d.NearLimit,
		MaxCosineDistance: m.d.MaxDistance + m.d.BorderlineWidth,
	})
	if err != nil {
		return fmt.Errorf("nearest neighbors: %w", err)
	}

	// Borderline detection: the nearest neighbor sits above MaxDistance but
	// inside the BorderlineWidth band. Push the pair so the reconciler can
	// ask LLMJudge whether the two posts are about the same event.
	if m.d.Borderline != nil {
		for _, n := range near {
			if n.PostID == in.PostID {
				continue
			}
			if n.Distance > m.d.MaxDistance && n.Distance <= m.d.MaxDistance+m.d.BorderlineWidth {
				_ = m.d.Borderline.Push(ctx, redisstore.BorderlinePair{
					PostID:      in.PostID,
					CandidateID: n.PostID,
					Distance:    n.Distance,
				})
			}
			break
		}
	}

	neighborIDs := make([]int64, 0, len(near))
	for _, n := range near {
		if n.PostID == in.PostID {
			continue
		}
		// Hard cut at MaxDistance — neighbors above the cap are borderline
		// (handled above) but must not auto-merge.
		if n.Distance > m.d.MaxDistance {
			continue
		}
		neighborIDs = append(neighborIDs, n.PostID)
	}
	if clusterID, ok, err := m.matchByCandidates(ctx, neighborIDs, in.PostID); err != nil {
		return err
	} else if ok {
		return m.finalize(ctx, in, sig, clusterID, "embedding")
	}

	clusterID, err := m.d.Clusters.Create(ctx)
	if err != nil {
		return fmt.Errorf("create cluster: %w", err)
	}
	if err := m.d.Posts.AttachCluster(ctx, in.PostID, clusterID); err != nil {
		return fmt.Errorf("attach cluster: %w", err)
	}
	if err := m.d.MinHashIndex.Add(ctx, in.PostID, sig); err != nil {
		return fmt.Errorf("minhash add: %w", err)
	}
	return m.d.Publisher.Publish(ctx, queue.SubjectClusterUpdate, map[string]any{
		"cluster_id": clusterID,
		"via":        "new",
		"ts":         time.Now().UTC(),
	})
}

// matchByCandidates walks candidates in order and returns the first one
// already attached to a cluster (skipping the incoming post itself). The ok
// return is false when no candidate has a cluster.
func (m *Matcher) matchByCandidates(ctx context.Context, candidates []int64, selfID int64) (int64, bool, error) {
	for _, cid := range candidates {
		if cid == selfID {
			continue
		}
		clusterID, err := m.d.Posts.GetClusterID(ctx, cid)
		if err != nil {
			// One bad lookup must not poison the rest of the candidate list.
			continue
		}
		if clusterID == 0 {
			continue
		}
		return clusterID, true, nil
	}
	return 0, false, nil
}

// finalize attaches the incoming post to clusterID, bumps coverage,
// registers its MinHash signature, and announces the update.
func (m *Matcher) finalize(ctx context.Context, in MatchInput, sig []uint32, clusterID int64, via string) error {
	if err := m.d.Posts.AttachCluster(ctx, in.PostID, clusterID); err != nil {
		return fmt.Errorf("attach cluster: %w", err)
	}
	if err := m.d.Clusters.IncrementCoverage(ctx, clusterID); err != nil {
		return fmt.Errorf("increment coverage: %w", err)
	}
	if err := m.d.MinHashIndex.Add(ctx, in.PostID, sig); err != nil {
		return fmt.Errorf("minhash add: %w", err)
	}
	return m.d.Publisher.Publish(ctx, queue.SubjectClusterUpdate, map[string]any{
		"cluster_id": clusterID,
		"via":        via,
		"ts":         time.Now().UTC(),
	})
}
