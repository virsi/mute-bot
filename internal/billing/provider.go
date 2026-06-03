// Package billing wraps payment providers behind a single interface so the
// bot-api process can grow new providers (TG Stars in M3, ЮКасса/Stripe
// later) without rewiring the handler code. The Provider implementations
// translate provider-specific payloads into a normalized Activation that
// billing.Service uses to persist a subscription row and grant Pro.
package billing

import (
	"context"
	"time"
)

// Activation is the normalized "user X paid for plan Y" event handed to
// billing.Service after a provider's HandlePayment parses a raw webhook.
//
// UserID carries the user's *Telegram* id (not the internal DB id) because
// the parsing layer does not have access to the users repo; billing.Service
// resolves it to the internal id before persisting.
type Activation struct {
	UserID      int64
	ProviderRef string
	Plan        string
	Duration    time.Duration
}

// Provider is the abstraction every payment backend implements.
//
//   - Name returns the stable string used as subscriptions.provider; it
//     must be unique across providers because it forms half of the
//     idempotency key.
//   - InvoiceURL returns the deeplink the user opens to pay. For TG Stars
//     this is the createInvoiceLink result. The bot wraps it in an inline
//     keyboard button.
//   - HandlePayment parses a provider-specific webhook payload and returns
//     a normalized Activation. Validation lives here (currency, amount,
//     plan code, signature when applicable).
type Provider interface {
	Name() string
	InvoiceURL(ctx context.Context, userID int64, plan string) (string, error)
	HandlePayment(ctx context.Context, raw []byte) (Activation, error)
}
