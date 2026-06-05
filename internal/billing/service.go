package billing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/virsi/mute-bot/internal/storage/postgres"
)

// ErrProviderUnknown is returned when CreateInvoice/Settle receives a
// provider name that was not registered in Deps. Callers use this with
// errors.Is to surface "YooKassa not configured" as a 4xx instead of a
// 5xx upstream.
var ErrProviderUnknown = errors.New("billing: unknown provider")

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
//
// As of P3-M2 Service holds a *map* of providers (key == Provider.Name())
// so the same Service can dispatch /buy and webhooks to either Stars or
// YooKassa. The legacy single-Provider field on Deps still works — it is
// merged into the map at construction time.
type Service struct {
	providers map[string]Provider
	subs      SubsRepo
	users     Users
	logger    *slog.Logger
	now       func() time.Time
}

// Deps configures NewService. Subs and Users are mandatory; at least one
// Provider must be supplied (via Provider or Providers). Logger and Now
// fall back to slog.Default / time.Now.
type Deps struct {
	// Provider is the legacy single-provider field used by Phase-2
	// wiring. When non-nil it is merged into Providers under
	// Provider.Name().
	Provider Provider
	// Providers is the multi-provider map keyed by Provider.Name().
	// Optional; merged with Provider.
	Providers map[string]Provider
	Subs      SubsRepo
	Users     Users
	Logger    *slog.Logger
	Now       func() time.Time
}

// NewService constructs a Service.
func NewService(d Deps) *Service {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	providers := make(map[string]Provider, len(d.Providers)+1)
	for k, v := range d.Providers {
		providers[k] = v
	}
	if d.Provider != nil {
		providers[d.Provider.Name()] = d.Provider
	}
	return &Service{
		providers: providers,
		subs:      d.Subs,
		users:     d.Users,
		logger:    d.Logger,
		now:       d.Now,
	}
}

// CreateInvoice returns a paid-deeplink for the chosen (provider, plan).
// The bot wraps the URL in an inline keyboard button so the user can tap
// to pay. Returns ErrProviderUnknown when provider was not registered.
func (s *Service) CreateInvoice(ctx context.Context, provider string, userTGID int64, plan string) (string, error) {
	p, ok := s.providers[provider]
	if !ok {
		return "", fmt.Errorf("%w: %q", ErrProviderUnknown, provider)
	}
	url, err := p.InvoiceURL(ctx, userTGID, plan)
	if err != nil {
		return "", fmt.Errorf("invoice url: %w", err)
	}
	return url, nil
}

// Settle is the idempotent activation entry point. provider names which
// Provider parses raw; Stars uses "tg_stars", YooKassa uses "yookassa".
//
// Sequence:
//  1. Provider.HandlePayment validates and extracts Activation.
//  2. users.RegisterOnStart resolves the Telegram id to the internal id
//     (creating the row if the user has never run /start).
//  3. SubsRepo.Insert persists the subscription; the UNIQUE constraint
//     on (provider, provider_ref) discards duplicates and returns
//     isNew=false on conflict.
//  4. Pro is granted ONLY when isNew=true OR a catch-up is detected
//     (row present but user not Pro — covers the failure window where
//     the first webhook persisted but GrantPro errored).
//
// Returns granted=true when this call extended Pro time; false on a
// duplicate webhook that was already processed.
func (s *Service) Settle(ctx context.Context, provider string, raw []byte) (bool, error) {
	p, ok := s.providers[provider]
	if !ok {
		return false, fmt.Errorf("%w: %q", ErrProviderUnknown, provider)
	}
	a, err := p.HandlePayment(ctx, raw)
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
		UserID:          u.ID,
		Provider:        p.Name(),
		ProviderRef:     a.ProviderRef,
		Plan:            a.Plan,
		StartedAt:       now,
		ExpiresAt:       &expires,
		PaymentMethodID: a.PaymentMethodID,
	})
	if err != nil {
		return false, fmt.Errorf("insert subscription: %w", err)
	}
	if !isNew {
		// Duplicate (provider, provider_ref) — the row already exists.
		// Usually a no-op, but if the previous attempt inserted the row
		// and then failed before GrantPro returned, the user is still
		// on the free tier despite having paid. Re-check the tier and
		// catch up if needed.
		if s.users.IsPro(u) {
			s.logger.InfoContext(ctx, "billing: duplicate webhook, skipping grant",
				slog.String("provider", p.Name()),
				slog.String("provider_ref", a.ProviderRef),
				slog.Int64("user_id", u.ID),
			)
			return false, nil
		}
		s.logger.WarnContext(ctx, "billing: subscription row present but user not Pro, granting catch-up",
			slog.String("provider", p.Name()),
			slog.String("provider_ref", a.ProviderRef),
			slog.Int64("user_id", u.ID),
		)
	}
	if err := s.users.GrantPro(ctx, u.ID, a.Duration); err != nil {
		return false, fmt.Errorf("grant pro: %w", err)
	}
	s.logger.InfoContext(ctx, "billing: pro granted",
		slog.String("provider", p.Name()),
		slog.String("plan", a.Plan),
		slog.Int64("user_id", u.ID),
		slog.Duration("duration", a.Duration),
	)
	return true, nil
}
