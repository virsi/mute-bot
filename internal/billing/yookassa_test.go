package billing

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestYooKassaProvider_Name(t *testing.T) {
	p := NewYooKassaProvider(YooKassaDeps{})
	require.Equal(t, "yookassa", p.Name())
}

func TestYooKassaProvider_InvoiceURL_PostsToYooKassa(t *testing.T) {
	var gotBody map[string]any
	var gotAuth string
	var gotIdempotenceKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/v3/payments", r.URL.Path)
		gotAuth = r.Header.Get("Authorization")
		gotIdempotenceKey = r.Header.Get("Idempotence-Key")
		b, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(b, &gotBody))
		_, _ = w.Write([]byte(`{"id":"pay-1","status":"pending","confirmation":{"type":"redirect","confirmation_url":"https://yoomoney.ru/checkout/pay-1"}}`))
	}))
	defer srv.Close()

	p := NewYooKassaProvider(YooKassaDeps{
		ShopID:     "shop-1",
		SecretKey:  "secret-1",
		ReturnURL:  "https://t.me/mute_bot",
		WebhookURL: "https://bot.example.com/yookassa/webhook",
		BaseURL:    srv.URL,
		HTTPClient: http.DefaultClient,
	})

	url, err := p.InvoiceURL(context.Background(), 4242, PlanPro30dRUB)
	require.NoError(t, err)
	require.Equal(t, "https://yoomoney.ru/checkout/pay-1", url)
	// base64("shop-1:secret-1") = c2hvcC0xOnNlY3JldC0x
	require.Equal(t, "Basic c2hvcC0xOnNlY3JldC0x", gotAuth)
	require.NotEmpty(t, gotIdempotenceKey)

	amount := gotBody["amount"].(map[string]any)
	require.Equal(t, "150.00", amount["value"])
	require.Equal(t, "RUB", amount["currency"])
	require.Equal(t, true, gotBody["save_payment_method"])
	require.Equal(t, true, gotBody["capture"])
	meta := gotBody["metadata"].(map[string]any)
	require.Equal(t, "4242", meta["tg_user_id"])
	require.Equal(t, PlanPro30dRUB, meta["plan"])
	conf := gotBody["confirmation"].(map[string]any)
	require.Equal(t, "redirect", conf["type"])
	require.Equal(t, "https://t.me/mute_bot", conf["return_url"])
}

func TestYooKassaProvider_InvoiceURL_RejectsUnknownPlan(t *testing.T) {
	p := NewYooKassaProvider(YooKassaDeps{ShopID: "x", SecretKey: "y"})
	_, err := p.InvoiceURL(context.Background(), 1, "weird")
	require.Error(t, err)
}

func TestYooKassaProvider_InvoiceURL_RejectsZeroUser(t *testing.T) {
	p := NewYooKassaProvider(YooKassaDeps{ShopID: "x", SecretKey: "y"})
	_, err := p.InvoiceURL(context.Background(), 0, PlanPro30dRUB)
	require.Error(t, err)
}

func TestYooKassaProvider_InvoiceURL_PropagatesHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"description":"boom"}`))
	}))
	defer srv.Close()
	p := NewYooKassaProvider(YooKassaDeps{
		ShopID: "shop-1", SecretKey: "secret-1",
		BaseURL: srv.URL, HTTPClient: http.DefaultClient,
	})
	_, err := p.InvoiceURL(context.Background(), 1, PlanPro30dRUB)
	require.Error(t, err)
	require.Contains(t, err.Error(), "500")
}

func TestYooKassaProvider_HandlePayment_HappyPath(t *testing.T) {
	body := []byte(`{
        "event": "payment.succeeded",
        "object": {
            "id": "pay-99",
            "status": "succeeded",
            "amount": {"value": "150.00", "currency": "RUB"},
            "metadata": {"tg_user_id": "4242", "plan": "pro_30d_rub"},
            "payment_method": {"id": "pm-1", "saved": true}
        }
    }`)
	p := NewYooKassaProvider(YooKassaDeps{})
	a, err := p.HandlePayment(context.Background(), body)
	require.NoError(t, err)
	require.Equal(t, int64(4242), a.UserID)
	require.Equal(t, "pay-99", a.ProviderRef)
	require.Equal(t, PlanPro30dRUB, a.Plan)
	require.Equal(t, Duration30d, a.Duration)
	require.Equal(t, "pm-1", a.PaymentMethodID)
}

func TestYooKassaProvider_HandlePayment_RejectsWrongEvent(t *testing.T) {
	body := []byte(`{"event":"payment.canceled","object":{"id":"x"}}`)
	p := NewYooKassaProvider(YooKassaDeps{})
	_, err := p.HandlePayment(context.Background(), body)
	require.Error(t, err)
	require.Contains(t, err.Error(), "event")
}

func TestYooKassaProvider_HandlePayment_RejectsWrongStatus(t *testing.T) {
	body := []byte(`{
        "event": "payment.succeeded",
        "object": {"id":"p","status":"pending","amount":{"value":"150.00","currency":"RUB"},"metadata":{"tg_user_id":"1","plan":"pro_30d_rub"}}
    }`)
	p := NewYooKassaProvider(YooKassaDeps{})
	_, err := p.HandlePayment(context.Background(), body)
	require.Error(t, err)
}

func TestYooKassaProvider_HandlePayment_RejectsWrongCurrency(t *testing.T) {
	body := []byte(`{
        "event": "payment.succeeded",
        "object": {"id":"p","status":"succeeded","amount":{"value":"150.00","currency":"USD"},"metadata":{"tg_user_id":"1","plan":"pro_30d_rub"}}
    }`)
	p := NewYooKassaProvider(YooKassaDeps{})
	_, err := p.HandlePayment(context.Background(), body)
	require.Error(t, err)
}

func TestYooKassaProvider_HandlePayment_RejectsWrongAmount(t *testing.T) {
	body := []byte(`{
        "event": "payment.succeeded",
        "object": {"id":"p","status":"succeeded","amount":{"value":"1.00","currency":"RUB"},"metadata":{"tg_user_id":"1","plan":"pro_30d_rub"}}
    }`)
	p := NewYooKassaProvider(YooKassaDeps{})
	_, err := p.HandlePayment(context.Background(), body)
	require.Error(t, err)
}

func TestYooKassaProvider_HandlePayment_RejectsUnknownPlan(t *testing.T) {
	body := []byte(`{
        "event": "payment.succeeded",
        "object": {"id":"p","status":"succeeded","amount":{"value":"150.00","currency":"RUB"},"metadata":{"tg_user_id":"1","plan":"weird"}}
    }`)
	p := NewYooKassaProvider(YooKassaDeps{})
	_, err := p.HandlePayment(context.Background(), body)
	require.Error(t, err)
}

func TestYooKassaProvider_HandlePayment_RejectsMissingID(t *testing.T) {
	body := []byte(`{
        "event": "payment.succeeded",
        "object": {"id":"","status":"succeeded","amount":{"value":"150.00","currency":"RUB"},"metadata":{"tg_user_id":"1","plan":"pro_30d_rub"}}
    }`)
	p := NewYooKassaProvider(YooKassaDeps{})
	_, err := p.HandlePayment(context.Background(), body)
	require.Error(t, err)
}

func TestYooKassaProvider_HandlePayment_RejectsBadUserID(t *testing.T) {
	body := []byte(`{
        "event": "payment.succeeded",
        "object": {"id":"p","status":"succeeded","amount":{"value":"150.00","currency":"RUB"},"metadata":{"tg_user_id":"abc","plan":"pro_30d_rub"}}
    }`)
	p := NewYooKassaProvider(YooKassaDeps{})
	_, err := p.HandlePayment(context.Background(), body)
	require.Error(t, err)
}

func TestYooKassaProvider_Renew_UsesSavedMethod(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v3/payments", r.URL.Path)
		require.NotEmpty(t, r.Header.Get("Idempotence-Key"))
		b, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(b, &gotBody))
		_, _ = w.Write([]byte(`{"id":"pay-2","status":"succeeded"}`))
	}))
	defer srv.Close()
	p := NewYooKassaProvider(YooKassaDeps{
		ShopID:     "shop-1",
		SecretKey:  "secret-1",
		BaseURL:    srv.URL,
		HTTPClient: http.DefaultClient,
	})
	paymentID, err := p.Renew(context.Background(), 4242, "pm-1")
	require.NoError(t, err)
	require.Equal(t, "pay-2", paymentID)
	require.Equal(t, "pm-1", gotBody["payment_method_id"])
	require.Equal(t, true, gotBody["capture"])
	require.Nil(t, gotBody["confirmation"], "renew must not require user redirect")
	require.Contains(t, strings.ToLower(gotBody["description"].(string)), "pro")
	meta := gotBody["metadata"].(map[string]any)
	require.Equal(t, "4242", meta["tg_user_id"])
	require.Equal(t, "1", meta["renewal"])
}

func TestYooKassaProvider_Renew_RejectsEmptyPaymentMethod(t *testing.T) {
	p := NewYooKassaProvider(YooKassaDeps{ShopID: "x", SecretKey: "y"})
	_, err := p.Renew(context.Background(), 1, "")
	require.Error(t, err)
}
