package postgres

import (
	"context"
	"fmt"
)

// DeliveriesRepo records which clusters were delivered to which users.
// Used to enforce anti-repeat: the digest assembler excludes cluster IDs
// already in this table for the target user.
type DeliveriesRepo struct{ p *Pool }

// NewDeliveriesRepo constructs a DeliveriesRepo bound to p.
func NewDeliveriesRepo(p *Pool) *DeliveriesRepo { return &DeliveriesRepo{p: p} }

// Record stores a delivery. ON CONFLICT DO NOTHING makes the call idempotent
// against bot-send retries.
func (r *DeliveriesRepo) Record(ctx context.Context, userID, clusterID int64, channel string) error {
	const q = `
		INSERT INTO deliveries (user_id, cluster_id, channel)
		VALUES ($1, $2, $3)
		ON CONFLICT (user_id, cluster_id) DO NOTHING`
	if _, err := r.p.Pool().Exec(ctx, q, userID, clusterID, channel); err != nil {
		return fmt.Errorf("record delivery: %w", err)
	}
	return nil
}

// ListClusterIDs returns the most-recently delivered cluster ids for userID,
// newest first, capped by limit. Used to build the anti-repeat exclusion set.
func (r *DeliveriesRepo) ListClusterIDs(ctx context.Context, userID int64, limit int) ([]int64, error) {
	const q = `
		SELECT cluster_id
		FROM deliveries
		WHERE user_id = $1
		ORDER BY delivered_at DESC
		LIMIT $2`
	rows, err := r.p.Pool().Query(ctx, q, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("list cluster ids: %w", err)
	}
	defer rows.Close()
	out := make([]int64, 0, limit)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows err: %w", err)
	}
	return out, nil
}
