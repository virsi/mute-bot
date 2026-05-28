package postgres

import (
	"context"
	"fmt"
	"time"
)

// Settings is the persistence-side per-user settings row.
type Settings struct {
	UserID         int64
	Topics         []string
	Threshold      int
	ScheduleJSON   []byte
	AlertsEnabled  bool
	AlertThreshold int
	WeeklyEnabled  bool
	UpdatedAt      time.Time
}

// SettingsUpdate carries the fields Upsert writes.
type SettingsUpdate struct {
	Topics         []string
	Threshold      int
	ScheduleJSON   []byte
	AlertsEnabled  bool
	AlertThreshold int
	WeeklyEnabled  bool
}

// SettingsRepo persists user_settings rows.
type SettingsRepo struct{ p *Pool }

// NewSettingsRepo constructs a SettingsRepo bound to p.
func NewSettingsRepo(p *Pool) *SettingsRepo { return &SettingsRepo{p: p} }

// Upsert writes a settings row for userID, replacing any existing row.
func (r *SettingsRepo) Upsert(ctx context.Context, userID int64, in SettingsUpdate) error {
	const q = `
		INSERT INTO user_settings (user_id, topics, threshold, digest_schedule,
		                           alerts_enabled, alert_threshold, weekly_enabled, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, now())
		ON CONFLICT (user_id) DO UPDATE
		   SET topics = EXCLUDED.topics,
		       threshold = EXCLUDED.threshold,
		       digest_schedule = EXCLUDED.digest_schedule,
		       alerts_enabled = EXCLUDED.alerts_enabled,
		       alert_threshold = EXCLUDED.alert_threshold,
		       weekly_enabled = EXCLUDED.weekly_enabled,
		       updated_at = now()`
	if _, err := r.p.Pool().Exec(ctx, q,
		userID, in.Topics, in.Threshold, in.ScheduleJSON,
		in.AlertsEnabled, in.AlertThreshold, in.WeeklyEnabled,
	); err != nil {
		return fmt.Errorf("upsert settings: %w", err)
	}
	return nil
}

// Get fetches the settings row for userID.
func (r *SettingsRepo) Get(ctx context.Context, userID int64) (Settings, error) {
	const q = `
		SELECT user_id, topics, threshold, digest_schedule,
		       alerts_enabled, alert_threshold, weekly_enabled, updated_at
		FROM user_settings
		WHERE user_id = $1`
	var s Settings
	if err := r.p.Pool().QueryRow(ctx, q, userID).Scan(
		&s.UserID, &s.Topics, &s.Threshold, &s.ScheduleJSON,
		&s.AlertsEnabled, &s.AlertThreshold, &s.WeeklyEnabled, &s.UpdatedAt,
	); err != nil {
		return Settings{}, fmt.Errorf("get settings: %w", err)
	}
	return s, nil
}
