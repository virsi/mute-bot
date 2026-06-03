// Package users wraps the postgres users + settings repos behind a single
// service that the bot, scheduler, and billing layers share. The Phase-2
// surface is intentionally small: register a user on /start, ask whether
// they are Pro, grant Pro for a duration, and bulk-downgrade expired Pro
// users.
package users

import (
	"context"
	"time"

	"github.com/virsi/mute-bot/internal/storage/postgres"
)

// RW is the slice of postgres.UsersRepo the service depends on.
// Kept narrow so service tests can substitute a mock without dragging in
// a Postgres connection.
type RW interface {
	GetOrCreate(ctx context.Context, tgUserID int64, username string) (postgres.User, bool, error)
	GrantPro(ctx context.Context, id int64, dur time.Duration) error
	BulkDowngradeExpired(ctx context.Context, asOf time.Time) ([]int64, error)
}

// SettingsWriter is the slice used when seeding defaults for newly
// registered users. Only Upsert is needed; reads happen elsewhere.
type SettingsWriter interface {
	Upsert(ctx context.Context, userID int64, in postgres.SettingsUpdate) error
}
