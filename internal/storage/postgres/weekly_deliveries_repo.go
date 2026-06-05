package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// WeeklyDeliveriesRepo persists "this Pro user received cluster X in ISO
// week Y" rows used for weekly-digest anti-repeat. Each row is created in
// a single batch by WeeklyAssembler.BuildWeekly after a successful send.
type WeeklyDeliveriesRepo struct{ p *Pool }

// NewWeeklyDeliveriesRepo constructs a WeeklyDeliveriesRepo bound to p.
func NewWeeklyDeliveriesRepo(p *Pool) *WeeklyDeliveriesRepo {
	return &WeeklyDeliveriesRepo{p: p}
}

// InsertIfAbsent inserts (userID, clusterID, isoWeek) and returns true iff
// the row is new. UNIQUE(user_id, cluster_id, iso_week) makes this
// idempotent per cluster; the partial index on (user_id, iso_week) keeps
// HasWeekRow cheap.
func (r *WeeklyDeliveriesRepo) InsertIfAbsent(
	ctx context.Context, userID, clusterID int64, isoWeek string,
) (bool, error) {
	const q = `
		INSERT INTO weekly_deliveries (user_id, cluster_id, iso_week)
		VALUES ($1, $2, $3)
		ON CONFLICT (user_id, cluster_id, iso_week) DO NOTHING
		RETURNING id`
	var id int64
	err := r.p.Pool().QueryRow(ctx, q, userID, clusterID, isoWeek).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("insert weekly delivery: %w", err)
	}
	return true, nil
}

// HasWeekRow returns true iff the user already received any cluster in
// isoWeek. The cron uses this to make INV-7 explicit: even if gocron fires
// the job twice in the same minute (clock skew or process restart), the
// second call observes the row and exits.
func (r *WeeklyDeliveriesRepo) HasWeekRow(
	ctx context.Context, userID int64, isoWeek string,
) (bool, error) {
	const q = `SELECT EXISTS (
		SELECT 1 FROM weekly_deliveries
		WHERE user_id = $1 AND iso_week = $2)`
	var ok bool
	if err := r.p.Pool().QueryRow(ctx, q, userID, isoWeek).Scan(&ok); err != nil {
		return false, fmt.Errorf("has weekly row: %w", err)
	}
	return ok, nil
}

// ListClusterIDsSince returns cluster ids the user has already received as
// part of any weekly digest since the cutoff, newest first. Used as the
// secondary anti-repeat filter when building the next weekly: a cluster
// already covered in the previous week's top-10 is not promoted again.
func (r *WeeklyDeliveriesRepo) ListClusterIDsSince(
	ctx context.Context, userID int64, since time.Time, limit int,
) ([]int64, error) {
	if limit <= 0 {
		limit = 200
	}
	const q = `
		SELECT cluster_id
		  FROM weekly_deliveries
		 WHERE user_id = $1 AND delivered_at >= $2
		 ORDER BY delivered_at DESC
		 LIMIT $3`
	rows, err := r.p.Pool().Query(ctx, q, userID, since, limit)
	if err != nil {
		return nil, fmt.Errorf("list weekly cluster ids: %w", err)
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
