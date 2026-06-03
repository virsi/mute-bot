package billing

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/virsi/mute-bot/internal/storage/postgres"
)

// SubsRepo is the slice of postgres.SubscriptionsRepo the orchestrator
// needs. Kept narrow so unit tests can substitute a fake.
type SubsRepo interface {
	Insert(ctx context.Context, in postgres.SubscriptionInsert) (int64, bool, error)
}

// Users resolves Telegram users to internal ids, grants Pro time, and
// reports whether the user is currently Pro. Implemented by *users.Service
// in production; mocked in unit tests.
type Users interface {
	RegisterOnStart(ctx context.Context, tgUserID int64, username string) (postgres.User, bool, error)
	GrantPro(ctx context.Context, id int64, dur time.Duration) error
	IsPro(u postgres.User) bool
}

// Service is the bot-api-side façade over Provider + SubsRepo + Users.
//
// CreateInvoice builds a payment URL the bot wraps in an inline button.
// Settle is the idempotent webhook entry point: parse, persist, grant.
type Service struct {
	provider Provider
	subs     SubsRepo
	users    Users
	logger   *slog.Logger
	now      func() time.Time
}

// Deps configures NewService. Provider, Subs and Users are mandatory;
// Logger and Now fall back to slog.Default / time.Now.
type Deps struct {
	Provider Provider
	Subs     SubsRepo
	Users    Users
	Logger   *slog.Logger
	Now      func() time.Time
}

// NewService constructs a Service.
func NewService(d Deps) *Service {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Service{
		provider: d.Provider,
		subs:     d.Subs,
		users:    d.Users,
		logger:   d.Logger,
		now:      d.Now,
	}
}

// CreateInvoice returns a paid-deeplink for the caller-chosen plan. The
// bot wraps the URL in an inline keyboard button so the user can tap to
// pay; the link is one-shot from the user's perspective but the underlying
// Telegram invoice is reusable, so refusing or canceling is harmless.
func (s *Service) CreateInvoice(ctx context.Context, userTGID int64, plan string) (string, error) {
	url, err := s.provider.InvoiceURL(ctx, userTGID, plan)
	if err != nil {
		return "", fmt.Errorf("invoice url: %w", err)
	}
	return url, nil
}

// Settle is the idempotent activation entry point called from the
// successful_payment handler.
//
// Sequence:
//  1. Provider.HandlePayment validates and extracts Activation.
//  2. users.RegisterOnStart resolves the Telegram id to the internal id
//     (creating the row if the user has never run /start).
//  3. SubsRepo.Insert persists the subscription; the UNIQUE constraint
//     on (provider, provider_ref) discards duplicates and returns
//     isNew=false on conflict.
//  4. Pro is granted ONLY when isNew=true, so a retried webhook is a
//     no-op past the persistence layer.
//
// Returns granted=true when this call actually extended Pro time; false
// when the row already existed (duplicate webhook).
func (s *Service) Settle(ctx context.Context, raw []byte) (bool, error) {
	a, err := s.provider.HandlePayment(ctx, raw)
	if err != nil {
		return false, fmt.Errorf("handle payment: %w", err)
	}
	u, _, err := s.users.RegisterOnStart(ctx, a.UserID, "")
	if err != nil {
		return false, fmt.Errorf("resolve user: %w", err)
	}
	now := s.now()
	expires := now.Add(a.Duration)
	_, isNew, err := s.subs.Insert(ctx, postgres.SubscriptionInsert{
		UserID:      u.ID,
		Provider:    s.provider.Name(),
		ProviderRef: a.ProviderRef,
		Plan:        a.Plan,
		StartedAt:   now,
		ExpiresAt:   &expires,
	})
	if err != nil {
		return false, fmt.Errorf("insert subscription: %w", err)
	}
	if !isNew {
		// Duplicate (provider, provider_ref) — the row already exists from
		// an earlier webhook. Usually a no-op, but if the previous attempt
		// inserted the row and then failed before GrantPro returned, the
		// user is still on the free tier despite having paid. Detect that
		// case by re-checking the tier and catching up.
		if s.users.IsPro(u) {
			s.logger.InfoContext(ctx, "billing: duplicate webhook, skipping grant",
				slog.String("provider", s.provider.Name()),
				slog.String("provider_ref", a.ProviderRef),
				slog.Int64("user_id", u.ID),
			)
			return false, nil
		}
		s.logger.WarnContext(ctx, "billing: subscription row present but user not Pro, granting catch-up",
			slog.String("provider", s.provider.Name()),
			slog.String("provider_ref", a.ProviderRef),
			slog.Int64("user_id", u.ID),
		)
	}
	if err := s.users.GrantPro(ctx, u.ID, a.Duration); err != nil {
		return false, fmt.Errorf("grant pro: %w", err)
	}
	s.logger.InfoContext(ctx, "billing: pro granted",
		slog.String("plan", a.Plan),
		slog.Int64("user_id", u.ID),
		slog.Duration("duration", a.Duration),
	)
	return true, nil
}
