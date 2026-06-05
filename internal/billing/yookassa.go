package billing

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// Plan constants for the RUB channel.
const (
	// PlanPro30dRUB names the 30-day Pro plan sold in rubles via YooKassa.
	PlanPro30dRUB = "pro_30d_rub"
	// PriceRUB150 is the per-month price string in YooKassa's required
	// "1234.56" decimal-with-two-fraction-digits format.
	PriceRUB150 = "150.00"

	yooKassaCurrency    = "RUB"
	yooKassaProvider    = "yookassa"
	yooKassaDefaultBase = "https://api.yookassa.ru"
	yooKassaPath        = "/v3/payments"

	yooKassaProductTitle = "Pro подписка 30 дней"
	yooKassaProductDesc  = "Pro: кастомные темы, alerts, weekly digest, безлимит /digest."
)

// YooKassaDeps configures NewYooKassaProvider. ShopID and SecretKey are
// required for InvoiceURL/Renew to dial YooKassa; HandlePayment works
// without them so unit tests can exercise the parser in isolation.
type YooKassaDeps struct {
	ShopID    string
	SecretKey string
	// WebhookURL is the absolute URL where YooKassa POSTs notifications,
	// e.g. "https://bot.example.com/yookassa/webhook". Currently unused
	// by InvoiceURL (YooKassa does not accept a webhook URL per request —
	// it is configured in the merchant dashboard) but kept on Deps so
	// future YooKassa API versions can adopt it without breaking callers.
	WebhookURL string
	// ReturnURL is the deeplink YooKassa redirects the user to after the
	// payment confirmation page. Defaults to https://t.me/.
	ReturnURL string
	// BaseURL overrides the YooKassa API base; defaults to
	// https://api.yookassa.ru. Tests inject a httptest.Server URL.
	BaseURL string
	// HTTPClient overrides the default http.Client (15s timeout) so tests
	// can pin transport behavior.
	HTTPClient *http.Client
}

// YooKassaProvider implements Provider over the YooKassa REST API.
//
// Wire as the "yookassa" provider in billing.Service. The first successful
// payment captures a payment_method_id in the notification body — the
// renewer uses that id to charge the saved card without user interaction
// when the subscription is about to expire.
type YooKassaProvider struct {
	shopID     string
	secretKey  string
	webhookURL string
	returnURL  string
	baseURL    string
	http       *http.Client
}

// NewYooKassaProvider constructs a YooKassaProvider applying sensible
// defaults: production YooKassa base URL, https://t.me/ return URL and a
// 15s-timeout http.Client when not overridden.
func NewYooKassaProvider(d YooKassaDeps) *YooKassaProvider {
	if d.BaseURL == "" {
		d.BaseURL = yooKassaDefaultBase
	}
	if d.ReturnURL == "" {
		d.ReturnURL = "https://t.me/"
	}
	if d.HTTPClient == nil {
		d.HTTPClient = &http.Client{Timeout: 15 * time.Second}
	}
	return &YooKassaProvider{
		shopID:     d.ShopID,
		secretKey:  d.SecretKey,
		webhookURL: d.WebhookURL,
		returnURL:  d.ReturnURL,
		baseURL:    d.BaseURL,
		http:       d.HTTPClient,
	}
}

// Name returns the persistence-side provider tag.
func (p *YooKassaProvider) Name() string { return yooKassaProvider }

// yooKassaPaymentResp is the relevant subset of the POST /v3/payments
// response. confirmation.confirmation_url is present only when the caller
// asked for a redirect confirmation; renewals (with payment_method_id) skip
// confirmation entirely and the server returns status="succeeded".
type yooKassaPaymentResp struct {
	ID           string `json:"id"`
	Status       string `json:"status"`
	Confirmation *struct {
		Type            string `json:"type"`
		ConfirmationURL string `json:"confirmation_url"`
	} `json:"confirmation,omitempty"`
}

// InvoiceURL creates a new YooKassa payment with save_payment_method=true
// and returns the redirect URL the user must open to pay. The
// payment_method captured on the resulting notification is stored on the
// subscription row so YooKassaRenewer can autopay before expiry.
func (p *YooKassaProvider) InvoiceURL(ctx context.Context, userID int64, plan string) (string, error) {
	if plan != PlanPro30dRUB {
		return "", fmt.Errorf("yookassa: unknown plan %q (want %q)", plan, PlanPro30dRUB)
	}
	if userID == 0 {
		return "", fmt.Errorf("yookassa: zero user id")
	}
	body := map[string]any{
		"amount":              map[string]string{"value": PriceRUB150, "currency": yooKassaCurrency},
		"capture":             true,
		"save_payment_method": true,
		"confirmation": map[string]string{
			"type":       "redirect",
			"return_url": p.returnURL,
		},
		"description": yooKassaProductDesc,
		"metadata": map[string]any{
			"tg_user_id": strconv.FormatInt(userID, 10),
			"plan":       plan,
		},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("marshal payment body: %w", err)
	}
	resp, err := p.do(ctx, http.MethodPost, yooKassaPath, raw)
	if err != nil {
		return "", err
	}
	var parsed yooKassaPaymentResp
	if err := json.Unmarshal(resp, &parsed); err != nil {
		return "", fmt.Errorf("unmarshal payment resp: %w", err)
	}
	if parsed.Confirmation == nil || parsed.Confirmation.ConfirmationURL == "" {
		return "", fmt.Errorf("yookassa: missing confirmation_url (status=%q)", parsed.Status)
	}
	return parsed.Confirmation.ConfirmationURL, nil
}

// yooKassaNotification is the trimmed view of the webhook payload.
type yooKassaNotification struct {
	Event  string `json:"event"`
	Object struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Amount struct {
			Value    string `json:"value"`
			Currency string `json:"currency"`
		} `json:"amount"`
		Metadata struct {
			TGUserID string `json:"tg_user_id"`
			Plan     string `json:"plan"`
		} `json:"metadata"`
		PaymentMethod struct {
			ID    string `json:"id"`
			Saved bool   `json:"saved"`
		} `json:"payment_method"`
	} `json:"object"`
}

// HandlePayment parses a YooKassa notification and returns a normalized
// Activation. Validates event == "payment.succeeded", status ==
// "succeeded", currency, amount, plan code; rejects everything else.
// HMAC verification is handled by YooKassaWebhook before HandlePayment
// runs — this function trusts the body it receives.
func (p *YooKassaProvider) HandlePayment(_ context.Context, raw []byte) (Activation, error) {
	var n yooKassaNotification
	if err := json.Unmarshal(raw, &n); err != nil {
		return Activation{}, fmt.Errorf("unmarshal notification: %w", err)
	}
	if n.Event != "payment.succeeded" {
		return Activation{}, fmt.Errorf("expected event payment.succeeded, got %q", n.Event)
	}
	if n.Object.Status != "succeeded" {
		return Activation{}, fmt.Errorf("expected status succeeded, got %q", n.Object.Status)
	}
	if n.Object.Amount.Currency != yooKassaCurrency {
		return Activation{}, fmt.Errorf("expected currency %s, got %q", yooKassaCurrency, n.Object.Amount.Currency)
	}
	if n.Object.Amount.Value != PriceRUB150 {
		return Activation{}, fmt.Errorf("expected amount %s, got %q", PriceRUB150, n.Object.Amount.Value)
	}
	if n.Object.Metadata.Plan != PlanPro30dRUB {
		return Activation{}, fmt.Errorf("unsupported plan %q", n.Object.Metadata.Plan)
	}
	if n.Object.ID == "" {
		return Activation{}, fmt.Errorf("missing payment id")
	}
	tgID, err := strconv.ParseInt(n.Object.Metadata.TGUserID, 10, 64)
	if err != nil || tgID == 0 {
		return Activation{}, fmt.Errorf("bad tg_user_id %q", n.Object.Metadata.TGUserID)
	}
	return Activation{
		UserID:          tgID,
		ProviderRef:     n.Object.ID,
		Plan:            PlanPro30dRUB,
		Duration:        Duration30d,
		PaymentMethodID: n.Object.PaymentMethod.ID,
	}, nil
}

// Renew creates a new YooKassa payment that charges paymentMethodID
// without user interaction. The caller passes a previously-saved
// payment_method_id (captured on the first successful payment); YooKassa
// returns the new payment id which then arrives via a webhook as a
// payment.succeeded event, going through the same Settle path as the
// initial payment.
//
// Returns the payment id so the renewer can log it.
//
// The deterministic Idempotence-Key derived from (subscription_id,
// expires_at_unix) means a renewer that ticks hourly and keeps picking
// the same row out of ListExpiring sends the SAME Idempotence-Key on
// every retry — YooKassa collapses them onto a single charge instead
// of billing the card N times in the 24h pre-expiry window.
func (p *YooKassaProvider) Renew(
	ctx context.Context, userID, subscriptionID int64, expiresAt time.Time, paymentMethodID string,
) (string, error) {
	if paymentMethodID == "" {
		return "", fmt.Errorf("yookassa renew: missing payment_method_id")
	}
	body := map[string]any{
		"amount":            map[string]string{"value": PriceRUB150, "currency": yooKassaCurrency},
		"capture":           true,
		"payment_method_id": paymentMethodID,
		"description":       yooKassaProductTitle,
		"metadata": map[string]any{
			"tg_user_id": strconv.FormatInt(userID, 10),
			"plan":       PlanPro30dRUB,
			"renewal":    "1",
		},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("marshal renew body: %w", err)
	}
	key := renewIdempotenceKey(subscriptionID, expiresAt)
	resp, err := p.doWithKey(ctx, http.MethodPost, yooKassaPath, raw, key)
	if err != nil {
		return "", err
	}
	var parsed yooKassaPaymentResp
	if err := json.Unmarshal(resp, &parsed); err != nil {
		return "", fmt.Errorf("unmarshal renew resp: %w", err)
	}
	return parsed.ID, nil
}

// renewIdempotenceKey derives a stable 32-char hex string from the
// subscription id and its current expiration time. Two renewal attempts
// before the row is replaced (so before YooKassa's webhook persists the
// new subscription) share the same key, and YooKassa collapses them.
func renewIdempotenceKey(subscriptionID int64, expiresAt time.Time) string {
	h := sha256.Sum256(fmt.Appendf(nil, "renew:%d:%d", subscriptionID, expiresAt.Unix()))
	return hex.EncodeToString(h[:16])
}

// do issues an authenticated request to YooKassa with a fresh
// Idempotence-Key. Returns the raw response body or an error when the
// HTTP status is non-2xx.
func (p *YooKassaProvider) do(ctx context.Context, method, path string, body []byte) ([]byte, error) {
	key, err := randomHex16()
	if err != nil {
		return nil, fmt.Errorf("idempotence key: %w", err)
	}
	return p.doWithKey(ctx, method, path, body, key)
}

// doWithKey is the shared transport that takes the Idempotence-Key as
// an argument so renewals can reuse a deterministic key.
func (p *YooKassaProvider) doWithKey(ctx context.Context, method, path string, body []byte, key string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, p.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	req.SetBasicAuth(p.shopID, p.secretKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotence-Key", key)
	resp, err := p.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("yookassa http: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("yookassa http %d: %s", resp.StatusCode, string(raw))
	}
	return raw, nil
}

// randomHex16 returns 16 hex chars suitable as an Idempotence-Key. crypto/
// rand guarantees uniqueness for the duration of a process and across
// retries — exactly what YooKassa expects so duplicate POSTs land on the
// same payment row instead of creating two charges.
func randomHex16() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
