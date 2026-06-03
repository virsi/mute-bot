package billing

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// Public constants for the Stars pricing knob set agreed in Phase-2 spec.
const (
	// PlanPro30d names the only Pro plan shipped in M3.
	PlanPro30d = "pro_30d"
	// PriceStars99 is the per-month price in XTR Stars.
	PriceStars99 = 99
	// Duration30d is the wall-clock length of the subscription period.
	Duration30d = 30 * 24 * time.Hour
)

const (
	starsProvider     = "tg_stars"
	starsCurrency     = "XTR"
	starsSubPeriodSec = 30 * 24 * 60 * 60 // 2592000

	// ruRU strings live in the source file deliberately: Phase-2 ships
	// ruRU-only, the bot is single-locale, and Telegram requires the
	// title/description on every createInvoiceLink call. Reading them
	// from i18n YAML would buy nothing right now.
	starsInvoiceTitle = "Pro подписка"
	starsInvoiceDesc  = "Pro подписка 30 дней: кастомные темы, alerts, безлимит /digest."
	starsPriceLabel   = "Pro подписка 30 дней"
)

// BotInvoiceClient is the narrow surface StarsProvider needs from the
// go-telegram/bot client. Defined as an interface so unit tests can
// substitute a fake without dialing api.telegram.org.
//
// The single method maps 1:1 to (b *Bot).CreateInvoiceLink in
// github.com/go-telegram/bot.
type BotInvoiceClient interface {
	CreateInvoiceLink(ctx context.Context, params *tgbot.CreateInvoiceLinkParams) (string, error)
}

// StarsProvider implements Provider over Telegram Stars (XTR). Invoices are
// issued via createInvoiceLink with subscription_period=2592000 so
// Telegram drives the monthly auto-renew; webhooks land in
// bot/payment_handlers.go which calls back into billing.Service.
type StarsProvider struct {
	bot BotInvoiceClient
}

// NewStarsProvider constructs a StarsProvider bound to the Bot API client.
// bot must be non-nil; pass *tgbot.Bot from cmd/bot-api.
func NewStarsProvider(bot BotInvoiceClient) *StarsProvider {
	return &StarsProvider{bot: bot}
}

// Name returns the persistence-side provider tag.
func (s *StarsProvider) Name() string { return starsProvider }

// InvoiceURL asks the Bot API for a t.me/$invoice/... deeplink the user
// taps to pay 99 XTR. The payload encodes the plan code and the user's
// Telegram id so HandlePayment can recover both from successful_payment
// (Telegram echoes the payload verbatim on the webhook).
func (s *StarsProvider) InvoiceURL(ctx context.Context, userID int64, plan string) (string, error) {
	if plan != PlanPro30d {
		return "", fmt.Errorf("stars: unknown plan %q (want %q)", plan, PlanPro30d)
	}
	if userID == 0 {
		return "", fmt.Errorf("stars: zero user id")
	}
	params := &tgbot.CreateInvoiceLinkParams{
		Title:              starsInvoiceTitle,
		Description:        starsInvoiceDesc,
		Payload:            encodePayload(plan, userID),
		Currency:           starsCurrency,
		Prices:             []models.LabeledPrice{{Label: starsPriceLabel, Amount: PriceStars99}},
		SubscriptionPeriod: starsSubPeriodSec,
	}
	link, err := s.bot.CreateInvoiceLink(ctx, params)
	if err != nil {
		return "", fmt.Errorf("create invoice link: %w", err)
	}
	return link, nil
}

// SuccessfulPaymentPayload is the trimmed view of the TG successful_payment
// update bot/payment_handlers passes to HandlePayment. Only the fields
// needed for validation + Activation building are kept.
type SuccessfulPaymentPayload struct {
	UserID                  int64  `json:"user_id"`
	Currency                string `json:"currency"`
	TotalAmount             int    `json:"total_amount"`
	InvoicePayload          string `json:"invoice_payload"`
	ProviderPaymentChargeID string `json:"provider_payment_charge_id"`
	TelegramPaymentChargeID string `json:"telegram_payment_charge_id"`
}

// HandlePayment parses a serialised SuccessfulPaymentPayload, validates
// currency/amount/plan, and returns the normalized Activation.
//
// provider_ref preference: provider_payment_charge_id when set
// (matches the task brief), else telegram_payment_charge_id. For
// Stars payments TG sometimes leaves provider_payment_charge_id empty,
// and the telegram_payment_charge_id is documented as unique per
// payment so it serves the idempotency contract equally well.
func (s *StarsProvider) HandlePayment(_ context.Context, raw []byte) (Activation, error) {
	var p SuccessfulPaymentPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return Activation{}, fmt.Errorf("unmarshal successful_payment: %w", err)
	}
	if p.Currency != starsCurrency {
		return Activation{}, fmt.Errorf("expected currency %s, got %q", starsCurrency, p.Currency)
	}
	if p.TotalAmount != PriceStars99 {
		return Activation{}, fmt.Errorf("expected amount %d, got %d", PriceStars99, p.TotalAmount)
	}
	plan, payloadUserID, err := decodePayload(p.InvoicePayload)
	if err != nil {
		return Activation{}, fmt.Errorf("invoice payload: %w", err)
	}
	if plan != PlanPro30d {
		return Activation{}, fmt.Errorf("unsupported plan %q", plan)
	}
	userID := p.UserID
	if userID == 0 {
		userID = payloadUserID
	}
	if userID == 0 {
		return Activation{}, fmt.Errorf("missing user id (both top-level and payload)")
	}
	ref := p.ProviderPaymentChargeID
	if ref == "" {
		ref = p.TelegramPaymentChargeID
	}
	if ref == "" {
		return Activation{}, fmt.Errorf("missing payment charge id")
	}
	return Activation{
		UserID:      userID,
		ProviderRef: ref,
		Plan:        plan,
		Duration:    Duration30d,
	}, nil
}

// encodePayload formats the invoice payload Telegram echoes back on
// successful_payment. Format: `<plan>:<userID>`. The colon-separated form
// keeps the payload <= 128 bytes per Bot API limit.
func encodePayload(plan string, userID int64) string {
	return plan + ":" + strconv.FormatInt(userID, 10)
}

// decodePayload reverses encodePayload. Returns the plan code and user id.
func decodePayload(p string) (string, int64, error) {
	parts := strings.SplitN(p, ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", 0, fmt.Errorf("malformed payload %q (want plan:userID)", p)
	}
	uid, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return "", 0, fmt.Errorf("parse user id: %w", err)
	}
	return parts[0], uid, nil
}
