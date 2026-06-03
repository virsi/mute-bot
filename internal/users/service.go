package users

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/virsi/mute-bot/internal/storage/postgres"
)

// Service is the domain entry point for user-tier operations shared by the
// bot, scheduler, and billing wirings.
type Service struct {
	users    RW
	settings SettingsWriter
	now      func() time.Time
	logger   *slog.Logger
}

// Deps configures NewService. Now defaults to time.Now and Logger to
// slog.Default(); both are injectable to make tests deterministic.
type Deps struct {
	Users    RW
	Settings SettingsWriter
	Now      func() time.Time
	Logger   *slog.Logger
}

// NewService constructs a Service. Users and Settings must be non-nil;
// optional fields fall through to safe defaults.
func NewService(d Deps) *Service {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	return &Service{
		users:    d.Users,
		settings: d.Settings,
		now:      d.Now,
		logger:   d.Logger,
	}
}

// RegisterOnStart upserts the user row and, on first creation only, seeds
// default settings (topics [politics, it], threshold 50, schedule
// 08:00/19:00 MSK). Returns the resulting user, a created flag, and any
// error. Returning users keep their existing settings.
func (s *Service) RegisterOnStart(ctx context.Context, tgUserID int64, username string) (postgres.User, bool, error) {
	u, created, err := s.users.GetOrCreate(ctx, tgUserID, username)
	if err != nil {
		return postgres.User{}, false, fmt.Errorf("get_or_create: %w", err)
	}
	if !created {
		return u, false, nil
	}
	sched, err := json.Marshal(map[string]any{
		"times": []string{"08:00", "19:00"},
		"tz":    "Europe/Moscow",
	})
	if err != nil {
		return u, true, fmt.Errorf("marshal default schedule: %w", err)
	}
	if err := s.settings.Upsert(ctx, u.ID, postgres.SettingsUpdate{
		Topics:         []string{"politics", "it"},
		Threshold:      50,
		ScheduleJSON:   sched,
		AlertsEnabled:  false,
		AlertThreshold: 85,
	}); err != nil {
		return u, true, fmt.Errorf("seed settings: %w", err)
	}
	return u, true, nil
}

// IsPro returns true when u.Tier == "pro" AND either tier_until is null
// (lifetime) or strictly in the future as of the service clock.
func (s *Service) IsPro(u postgres.User) bool {
	if u.Tier != "pro" {
		return false
	}
	if u.TierUntil == nil {
		return true
	}
	return u.TierUntil.After(s.now())
}

// GrantPro extends the user's Pro period by dur. Delegates to the repo,
// which handles the "extend from later of (now, current tier_until)"
// math.
func (s *Service) GrantPro(ctx context.Context, id int64, dur time.Duration) error {
	if err := s.users.GrantPro(ctx, id, dur); err != nil {
		return fmt.Errorf("grant pro: %w", err)
	}
	return nil
}

// DowngradeExpired flips every Pro user whose tier_until has passed back
// to free, clearing tier_until in the same write. Returns the count of
// users actually downgraded. Per-user failures are logged at WARN and do
// not stop the sweep — the next tick will retry.
func (s *Service) DowngradeExpired(ctx context.Context) (int, error) {
	ids, err := s.users.ListExpired(ctx, s.now())
	if err != nil {
		return 0, fmt.Errorf("list expired: %w", err)
	}
	n := 0
	for _, id := range ids {
		if err := s.users.SetTier(ctx, id, "free", nil); err != nil {
			s.logger.WarnContext(ctx, "downgrade expired",
				slog.Int64("user_id", id), slog.Any("err", err))
			continue
		}
		n++
	}
	return n, nil
}
