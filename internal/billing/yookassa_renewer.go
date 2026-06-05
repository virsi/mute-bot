package billing

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/virsi/mute-bot/internal/storage/postgres"
)

// Renewer is the small surface YooKassaRenewer needs from YooKassaProvider.
// Keeping it as an interface keeps the renewer unit tests free of a real
// httptest server.
type Renewer interface {
	Renew(ctx context.Context, userID int64, paymentMethodID string) (string, error)
}

// SubsExpiringReader is the subset of postgres.SubscriptionsRepo the
// renewer reads. Narrow so tests can stub it without a real database.
type SubsExpiringReader interface {
	ListExpiring(ctx context.Context, window time.Duration) ([]postgres.ExpiringSubscription, error)
}

// YooKassaRenewerDeps configures NewYooKassaRenewer. Interval defaults to
// 1 hour and Window to 24 hours so a subscription that expires in <24h is
// auto-renewed before the user ever notices a downgrade. The renewal
// lands as a payment.succeeded webhook which the existing Settle path
// handles idempotently — no extra wiring needed for the activation
// itself.
type YooKassaRenewerDeps struct {
	Renewer  Renewer
	Subs     SubsExpiringReader
	Interval time.Duration
	Window   time.Duration
	Logger   *slog.Logger
}

// YooKassaRenewer periodically scans for Pro subscriptions about to
// expire that have a saved payment_method_id and calls Renewer.Renew to
// charge the saved card. The actual Pro extension happens when YooKassa
// emits the resulting payment.succeeded webhook, which goes through
// billing.Service.Settle and grants another 30 days idempotently.
type YooKassaRenewer struct {
	d YooKassaRenewerDeps
}

// NewYooKassaRenewer constructs a YooKassaRenewer with sensible defaults.
func NewYooKassaRenewer(d YooKassaRenewerDeps) *YooKassaRenewer {
	if d.Interval == 0 {
		d.Interval = time.Hour
	}
	if d.Window == 0 {
		d.Window = 24 * time.Hour
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	return &YooKassaRenewer{d: d}
}

// Run blocks until ctx is cancelled. Calls Step on every Interval tick.
// One immediate Step runs at start so a cold boot never waits a full
// Interval to catch subscriptions already in the renewal window.
func (r *YooKassaRenewer) Run(ctx context.Context) error {
	t := time.NewTicker(r.d.Interval)
	defer t.Stop()
	if err := r.Step(ctx); err != nil {
		r.d.Logger.WarnContext(ctx, "renewer initial step", slog.Any("err", err))
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			if err := r.Step(ctx); err != nil {
				r.d.Logger.WarnContext(ctx, "renewer step", slog.Any("err", err))
			}
		}
	}
}

// Step queries expiring subscriptions and triggers one Renew call per row.
// Errors on individual rows are logged WARN and skipped so a single bad
// payment_method_id does not block renewals for the rest of the cohort.
func (r *YooKassaRenewer) Step(ctx context.Context) error {
	rows, err := r.d.Subs.ListExpiring(ctx, r.d.Window)
	if err != nil {
		return fmt.Errorf("list expiring: %w", err)
	}
	for _, e := range rows {
		if e.Provider != "yookassa" {
			continue
		}
		paymentID, err := r.d.Renewer.Renew(ctx, e.TGUserID, e.PaymentMethodID)
		if err != nil {
			r.d.Logger.WarnContext(ctx, "renew failed",
				slog.Int64("user_id", e.UserID),
				slog.Int64("tg_user_id", e.TGUserID),
				slog.String("payment_method_id", e.PaymentMethodID),
				slog.Any("err", err))
			continue
		}
		r.d.Logger.InfoContext(ctx, "yookassa renewal initiated",
			slog.Int64("user_id", e.UserID),
			slog.String("payment_id", paymentID),
			slog.Time("expires_at", e.ExpiresAt))
	}
	return nil
}
