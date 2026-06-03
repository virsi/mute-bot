package billing

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	tgbot "github.com/go-telegram/bot"
)

// fakeInvoiceClient stubs BotInvoiceClient. Captures the params it was
// called with so tests can assert on currency/payload/period/etc.
type fakeInvoiceClient struct {
	link     string
	err      error
	gotParam *tgbot.CreateInvoiceLinkParams
}

func (f *fakeInvoiceClient) CreateInvoiceLink(_ context.Context, p *tgbot.CreateInvoiceLinkParams) (string, error) {
	f.gotParam = p
	if f.err != nil {
		return "", f.err
	}
	return f.link, nil
}

func TestStarsProvider_Name(t *testing.T) {
	require.Equal(t, "tg_stars", NewStarsProvider(&fakeInvoiceClient{}).Name())
}

func TestStarsProvider_InvoiceURL_Happy(t *testing.T) {
	fc := &fakeInvoiceClient{link: "https://t.me/$abcdef"}
	p := NewStarsProvider(fc)
	url, err := p.InvoiceURL(context.Background(), 12345, PlanPro30d)
	require.NoError(t, err)
	require.Equal(t, "https://t.me/$abcdef", url)
	require.NotNil(t, fc.gotParam)
	require.Equal(t, "XTR", fc.gotParam.Currency)
	require.Equal(t, "pro_30d:12345", fc.gotParam.Payload)
	require.Equal(t, 2592000, fc.gotParam.SubscriptionPeriod)
	require.Len(t, fc.gotParam.Prices, 1)
	require.Equal(t, 99, fc.gotParam.Prices[0].Amount)
}

func TestStarsProvider_InvoiceURL_UnknownPlan(t *testing.T) {
	p := NewStarsProvider(&fakeInvoiceClient{})
	_, err := p.InvoiceURL(context.Background(), 1, "pro_lifetime")
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown plan")
}

func TestStarsProvider_InvoiceURL_ZeroUserID(t *testing.T) {
	p := NewStarsProvider(&fakeInvoiceClient{})
	_, err := p.InvoiceURL(context.Background(), 0, PlanPro30d)
	require.Error(t, err)
}

func TestStarsProvider_InvoiceURL_ClientError(t *testing.T) {
	fc := &fakeInvoiceClient{err: errors.New("upstream 503")}
	p := NewStarsProvider(fc)
	_, err := p.InvoiceURL(context.Background(), 1, PlanPro30d)
	require.Error(t, err)
	require.Contains(t, err.Error(), "create invoice link")
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}

func TestStarsProvider_HandlePayment_HappyPath(t *testing.T) {
	p := NewStarsProvider(&fakeInvoiceClient{})
	raw := mustJSON(t, SuccessfulPaymentPayload{
		UserID: 42, Currency: "XTR", TotalAmount: 99,
		InvoicePayload: "pro_30d:42", ProviderPaymentChargeID: "prov-ref-1",
		TelegramPaymentChargeID: "tg-ref-1",
	})
	a, err := p.HandlePayment(context.Background(), raw)
	require.NoError(t, err)
	require.Equal(t, int64(42), a.UserID)
	require.Equal(t, "prov-ref-1", a.ProviderRef, "provider_payment_charge_id wins when set")
	require.Equal(t, "pro_30d", a.Plan)
	require.Equal(t, Duration30d, a.Duration)
}

func TestStarsProvider_HandlePayment_FallsBackToTelegramChargeID(t *testing.T) {
	p := NewStarsProvider(&fakeInvoiceClient{})
	raw := mustJSON(t, SuccessfulPaymentPayload{
		UserID: 42, Currency: "XTR", TotalAmount: 99,
		InvoicePayload: "pro_30d:42",
		// ProviderPaymentChargeID empty — Stars often leaves it blank.
		TelegramPaymentChargeID: "tg-ref-only",
	})
	a, err := p.HandlePayment(context.Background(), raw)
	require.NoError(t, err)
	require.Equal(t, "tg-ref-only", a.ProviderRef)
}

func TestStarsProvider_HandlePayment_UsesUserFromPayloadWhenTopLevelMissing(t *testing.T) {
	p := NewStarsProvider(&fakeInvoiceClient{})
	raw := mustJSON(t, SuccessfulPaymentPayload{
		// UserID intentionally 0 — payload still carries it.
		Currency: "XTR", TotalAmount: 99,
		InvoicePayload: "pro_30d:777", TelegramPaymentChargeID: "tg-ref",
	})
	a, err := p.HandlePayment(context.Background(), raw)
	require.NoError(t, err)
	require.Equal(t, int64(777), a.UserID)
}

func TestStarsProvider_HandlePayment_WrongCurrency(t *testing.T) {
	p := NewStarsProvider(&fakeInvoiceClient{})
	raw := mustJSON(t, SuccessfulPaymentPayload{
		UserID: 1, Currency: "USD", TotalAmount: 99,
		InvoicePayload: "pro_30d:1", TelegramPaymentChargeID: "x",
	})
	_, err := p.HandlePayment(context.Background(), raw)
	require.Error(t, err)
	require.Contains(t, err.Error(), "currency")
}

func TestStarsProvider_HandlePayment_WrongAmount(t *testing.T) {
	p := NewStarsProvider(&fakeInvoiceClient{})
	raw := mustJSON(t, SuccessfulPaymentPayload{
		UserID: 1, Currency: "XTR", TotalAmount: 1,
		InvoicePayload: "pro_30d:1", TelegramPaymentChargeID: "x",
	})
	_, err := p.HandlePayment(context.Background(), raw)
	require.Error(t, err)
	require.Contains(t, err.Error(), "amount")
}

func TestStarsProvider_HandlePayment_UnknownPlan(t *testing.T) {
	p := NewStarsProvider(&fakeInvoiceClient{})
	raw := mustJSON(t, SuccessfulPaymentPayload{
		UserID: 1, Currency: "XTR", TotalAmount: 99,
		InvoicePayload: "pro_lifetime:1", TelegramPaymentChargeID: "x",
	})
	_, err := p.HandlePayment(context.Background(), raw)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unsupported plan")
}

func TestStarsProvider_HandlePayment_MalformedPayload(t *testing.T) {
	p := NewStarsProvider(&fakeInvoiceClient{})
	raw := mustJSON(t, SuccessfulPaymentPayload{
		UserID: 1, Currency: "XTR", TotalAmount: 99,
		InvoicePayload: "no-colon-here", TelegramPaymentChargeID: "x",
	})
	_, err := p.HandlePayment(context.Background(), raw)
	require.Error(t, err)
}

func TestStarsProvider_HandlePayment_MissingChargeID(t *testing.T) {
	p := NewStarsProvider(&fakeInvoiceClient{})
	raw := mustJSON(t, SuccessfulPaymentPayload{
		UserID: 1, Currency: "XTR", TotalAmount: 99,
		InvoicePayload: "pro_30d:1",
		// Both charge id fields empty.
	})
	_, err := p.HandlePayment(context.Background(), raw)
	require.Error(t, err)
	require.Contains(t, err.Error(), "charge id")
}

func TestStarsProvider_HandlePayment_BadJSON(t *testing.T) {
	p := NewStarsProvider(&fakeInvoiceClient{})
	_, err := p.HandlePayment(context.Background(), []byte("{not json"))
	require.Error(t, err)
}
