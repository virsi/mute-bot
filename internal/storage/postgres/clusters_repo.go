package postgres

import (
	"context"
	"fmt"
	"time"
)

// Cluster is the persistence-side cluster row. Mirrors the schema.
type Cluster struct {
	ID            int64
	Headline      string
	Summary       string
	Topics        []string
	Severity      int
	Coverage      int
	Score         float32
	FirstSeenAt   time.Time
	LastUpdatedAt time.Time
	Status        string
}

// ClusterMeta carries the fields the classifier fills in via UpdateMeta.
type ClusterMeta struct {
	Headline string
	Summary  string
	Topics   []string
	Severity int
}

// ClusterFilter parameterises Search.
type ClusterFilter struct {
	Topics     []string
	MinScore   float32
	SinceTime  time.Time
	ExcludeIDs []int64
	Limit      int
}

// ClustersRepo persists news clusters built by the dedup pipeline.
type ClustersRepo struct{ p *Pool }

// NewClustersRepo constructs a ClustersRepo bound to p.
func NewClustersRepo(p *Pool) *ClustersRepo { return &ClustersRepo{p: p} }

// Create inserts an empty cluster row and returns its id. The dedup pipeline
// uses this to materialise a new cluster the first time it sees a new event.
func (r *ClustersRepo) Create(ctx context.Context) (int64, error) {
	var id int64
	if err := r.p.Pool().QueryRow(ctx,
		`INSERT INTO clusters DEFAULT VALUES RETURNING id`).Scan(&id); err != nil {
		return 0, fmt.Errorf("create cluster: %w", err)
	}
	return id, nil
}

// UpdateMeta sets the classifier-supplied fields and bumps last_updated_at.
func (r *ClustersRepo) UpdateMeta(ctx context.Context, id int64, m ClusterMeta) error {
	const q = `
		UPDATE clusters
		   SET headline = $2, summary = $3, topics = $4, severity = $5,
		       last_updated_at = now()
		 WHERE id = $1`
	if _, err := r.p.Pool().Exec(ctx, q, id, m.Headline, m.Summary, m.Topics, m.Severity); err != nil {
		return fmt.Errorf("update meta: %w", err)
	}
	return nil
}

// SetScore overwrites the ranker score and bumps last_updated_at.
func (r *ClustersRepo) SetScore(ctx context.Context, id int64, score float32) error {
	const q = `UPDATE clusters SET score = $2, last_updated_at = now() WHERE id = $1`
	if _, err := r.p.Pool().Exec(ctx, q, id, score); err != nil {
		return fmt.Errorf("set score: %w", err)
	}
	return nil
}

// IncrementCoverage bumps coverage by 1 and last_updated_at. Called when a
// new post joins an existing cluster.
func (r *ClustersRepo) IncrementCoverage(ctx context.Context, id int64) error {
	const q = `UPDATE clusters SET coverage = coverage + 1, last_updated_at = now() WHERE id = $1`
	if _, err := r.p.Pool().Exec(ctx, q, id); err != nil {
		return fmt.Errorf("increment coverage: %w", err)
	}
	return nil
}

// Get fetches a single cluster by id.
func (r *ClustersRepo) Get(ctx context.Context, id int64) (Cluster, error) {
	const q = `
		SELECT id, headline, summary, topics, severity, coverage, score,
		       first_seen_at, last_updated_at, status
		FROM clusters
		WHERE id = $1`
	var c Cluster
	if err := r.p.Pool().QueryRow(ctx, q, id).Scan(
		&c.ID, &c.Headline, &c.Summary, &c.Topics, &c.Severity, &c.Coverage, &c.Score,
		&c.FirstSeenAt, &c.LastUpdatedAt, &c.Status,
	); err != nil {
		return Cluster{}, fmt.Errorf("get cluster: %w", err)
	}
	return c, nil
}

// Merge moves every post attached to from onto into, sums coverage, and
// marks from.status = 'merged' so it is excluded from future search. The
// three writes share one transaction so a partial merge can never leak.
func (r *ClustersRepo) Merge(ctx context.Context, into, from int64) error {
	tx, err := r.p.Pool().Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx,
		`UPDATE posts SET cluster_id = $1 WHERE cluster_id = $2`, into, from); err != nil {
		return fmt.Errorf("move posts: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE clusters
		    SET coverage = coverage + (SELECT coverage FROM clusters WHERE id = $2),
		        last_updated_at = now()
		  WHERE id = $1`, into, from); err != nil {
		return fmt.Errorf("sum coverage: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE clusters SET status = 'merged', last_updated_at = now() WHERE id = $1`,
		from); err != nil {
		return fmt.Errorf("mark merged: %w", err)
	}
	return tx.Commit(ctx)
}

// Snapshot is the read model fed to the ranker. MaxAuthority is the highest
// authority score across the channels that have posted into the cluster.
type Snapshot struct {
	Coverage     int
	Severity     int
	MaxAuthority int
}

// Snapshot returns the ranking-relevant aggregates for clusterID. Joining
// through posts→channels keeps the query a single round-trip; the LEFT JOINs
// preserve the row when no posts are attached yet (coverage stays 0/severity
// stays whatever has been written, max_auth becomes 0).
func (r *ClustersRepo) Snapshot(ctx context.Context, id int64) (Snapshot, error) {
	const q = `
		SELECT c.coverage, c.severity,
		       COALESCE(MAX(ch.authority_score), 0) AS max_auth
		FROM clusters c
		LEFT JOIN posts    p  ON p.cluster_id = c.id
		LEFT JOIN channels ch ON ch.id = p.channel_id
		WHERE c.id = $1
		GROUP BY c.id`
	var s Snapshot
	if err := r.p.Pool().QueryRow(ctx, q, id).Scan(&s.Coverage, &s.Severity, &s.MaxAuthority); err != nil {
		return Snapshot{}, fmt.Errorf("snapshot cluster %d: %w", id, err)
	}
	return s, nil
}

// TopByScoreSince returns up to limit active clusters whose last_updated_at
// is >= since, score-descending. Empty topics means "no topic filter"
// (used when the user has no preset topic list); non-empty topics is
// applied as a set overlap.
//
// excludeIDs supports anti-repeat at the call site: pass cluster ids the
// user has already seen in earlier weekly digests within the same look-
// back so they do not re-appear at the top.
func (r *ClustersRepo) TopByScoreSince(
	ctx context.Context, since time.Time, topics []string,
	excludeIDs []int64, limit int,
) ([]Cluster, error) {
	if limit <= 0 {
		limit = 10
	}
	if excludeIDs == nil {
		excludeIDs = []int64{}
	}
	if topics == nil {
		topics = []string{}
	}
	const q = `
		SELECT id, headline, summary, topics, severity, coverage, score,
		       first_seen_at, last_updated_at, status
		  FROM clusters
		 WHERE status = 'active'
		   AND last_updated_at >= $1
		   AND ($2::text[] = '{}'::text[] OR topics && $2::text[])
		   AND NOT (id = ANY($3::bigint[]))
		 ORDER BY score DESC, last_updated_at DESC
		 LIMIT $4`
	rows, err := r.p.Pool().Query(ctx, q, since, topics, excludeIDs, limit)
	if err != nil {
		return nil, fmt.Errorf("top by score: %w", err)
	}
	defer rows.Close()
	out := make([]Cluster, 0, limit)
	for rows.Next() {
		var c Cluster
		if err := rows.Scan(&c.ID, &c.Headline, &c.Summary, &c.Topics, &c.Severity,
			&c.Coverage, &c.Score, &c.FirstSeenAt, &c.LastUpdatedAt, &c.Status); err != nil {
			return nil, fmt.Errorf("scan cluster: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows err: %w", err)
	}
	return out, nil
}

// Search returns active clusters matching the filter, score-descending.
// Topics is matched as set overlap (&&). Empty ExcludeIDs is fine — the
// generated empty bigint[] simply matches nothing.
func (r *ClustersRepo) Search(ctx context.Context, f ClusterFilter) ([]Cluster, error) {
	if f.ExcludeIDs == nil {
		f.ExcludeIDs = []int64{}
	}
	if f.Topics == nil {
		f.Topics = []string{}
	}
	const q = `
		SELECT id, headline, summary, topics, severity, coverage, score,
		       first_seen_at, last_updated_at, status
		FROM clusters
		WHERE status = 'active'
		  AND last_updated_at >= $1
		  AND score >= $2
		  AND topics && $3::text[]
		  AND NOT (id = ANY($4::bigint[]))
		ORDER BY score DESC
		LIMIT $5`
	rows, err := r.p.Pool().Query(ctx, q, f.SinceTime, f.MinScore, f.Topics, f.ExcludeIDs, f.Limit)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	defer rows.Close()
	out := make([]Cluster, 0, f.Limit)
	for rows.Next() {
		var c Cluster
		if err := rows.Scan(&c.ID, &c.Headline, &c.Summary, &c.Topics, &c.Severity, &c.Coverage,
			&c.Score, &c.FirstSeenAt, &c.LastUpdatedAt, &c.Status); err != nil {
			return nil, fmt.Errorf("scan cluster: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows err: %w", err)
	}
	return out, nil
}
