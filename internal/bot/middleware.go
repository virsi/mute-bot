package bot

import (
	"context"
	"fmt"

	"github.com/virsi/mute-bot/internal/storage/postgres"
)

// CommandHandler is the canonical signature of a slash-command method
// after it has resolved the user. Middleware wraps these to add cross-
// cutting concerns (tier gates, rate limits, audit logging) without
// touching each command body.
type CommandHandler func(ctx context.Context, tgUserID int64, username string) error

// proGateDeniedRu is the user-facing copy shown when a Free user hits a
// Pro-only command. /buy is the upgrade entry point added in P2-M3.
const proGateDeniedRu = "Эта команда доступна в Pro-подписке. Используй /buy"

// RequirePro wraps next so that only Pro users reach it. It resolves
// the user via the supplied Registrar (RegisterOnStart is idempotent —
// returning users get their existing row), asks the TierChecker, and
// either delegates to next or replies with the Pro-gate message.
//
// The closure depends only on the small interfaces in commands.go so it
// can be unit-tested without touching the full Handlers wiring. In
// production registerHandlers chooses which command gets the gate.
func RequirePro(registrar Registrar, tier TierChecker, sender SendAPI, next CommandHandler) CommandHandler {
	return func(ctx context.Context, tgUserID int64, username string) error {
		var (
			u   postgres.User
			err error
		)
		u, _, err = registrar.RegisterOnStart(ctx, tgUserID, username)
		if err != nil {
			return fmt.Errorf("resolve user: %w", err)
		}
		if tier.IsPro(u) {
			return next(ctx, tgUserID, username)
		}
		if err := sender.Send(ctx, tgUserID, proGateDeniedRu); err != nil {
			return fmt.Errorf("send gate reply: %w", err)
		}
		return nil
	}
}
