package billing

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// fakeWebhookSettler implements SettlerForWebhook so the HTTP handler can
// be exercised without a real billing.Service.
type fakeWebhookSettler struct {
	called   bool
	provider string
	raw      []byte
	granted  bool
	err      error
}

func (f *fakeWebhookSettler) Settle(_ context.Context, provider string, raw []byte) (bool, error) {
	f.called = true
	f.provider = provider
	f.raw = raw
	return f.granted, f.err
}

func sign(secret, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	return hex.EncodeToString(mac.Sum(nil))
}

func TestYooKassaWebhook_HappyPath(t *testing.T) {
	secret := "hmac-secret"
	settler := &fakeWebhookSettler{granted: true}
	h := NewYooKassaWebhook(YooKassaWebhookDeps{Settler: settler, Secret: secret})
	body := `{"event":"payment.succeeded","object":{"id":"p1"}}`
	req := httptest.NewRequest(http.MethodPost, "/yookassa/webhook", strings.NewReader(body))
	req.Header.Set("X-YooKassa-Signature", sign(secret, body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.True(t, settler.called)
	require.Equal(t, "yookassa", settler.provider)
	require.JSONEq(t, body, string(settler.raw))
}

func TestYooKassaWebhook_BadSignature_401(t *testing.T) {
	secret := "hmac-secret"
	settler := &fakeWebhookSettler{}
	h := NewYooKassaWebhook(YooKassaWebhookDeps{Settler: settler, Secret: secret})
	body := `{"event":"payment.succeeded"}`
	req := httptest.NewRequest(http.MethodPost, "/yookassa/webhook", strings.NewReader(body))
	req.Header.Set("X-YooKassa-Signature", "deadbeef")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.False(t, settler.called)
}

func TestYooKassaWebhook_MissingSignature_401(t *testing.T) {
	h := NewYooKassaWebhook(YooKassaWebhookDeps{Settler: &fakeWebhookSettler{}, Secret: "s"})
	req := httptest.NewRequest(http.MethodPost, "/yookassa/webhook", bytes.NewReader([]byte(`{}`)))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestYooKassaWebhook_WrongMethod_405(t *testing.T) {
	h := NewYooKassaWebhook(YooKassaWebhookDeps{Settler: &fakeWebhookSettler{}, Secret: "s"})
	req := httptest.NewRequest(http.MethodGet, "/yookassa/webhook", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	require.Equal(t, "POST", rec.Header().Get("Allow"))
}

func TestYooKassaWebhook_SettlerError_500(t *testing.T) {
	secret := "s"
	settler := &fakeWebhookSettler{err: errors.New("boom")}
	h := NewYooKassaWebhook(YooKassaWebhookDeps{Settler: settler, Secret: secret})
	body := `{"event":"payment.succeeded"}`
	req := httptest.NewRequest(http.MethodPost, "/yookassa/webhook", strings.NewReader(body))
	req.Header.Set("X-YooKassa-Signature", sign(secret, body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestYooKassaWebhook_DuplicateNoGrant_200(t *testing.T) {
	secret := "s"
	settler := &fakeWebhookSettler{granted: false} // duplicate
	h := NewYooKassaWebhook(YooKassaWebhookDeps{Settler: settler, Secret: secret})
	body := `{"event":"payment.succeeded"}`
	req := httptest.NewRequest(http.MethodPost, "/yookassa/webhook", strings.NewReader(body))
	req.Header.Set("X-YooKassa-Signature", sign(secret, body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code) // YooKassa retries on non-2xx
}

func TestYooKassaWebhook_CustomHeader(t *testing.T) {
	secret := "s"
	settler := &fakeWebhookSettler{granted: true}
	h := NewYooKassaWebhook(YooKassaWebhookDeps{
		Settler: settler, Secret: secret,
		SignatureHeader: "X-Custom-Sig",
	})
	body := `{"event":"payment.succeeded"}`
	req := httptest.NewRequest(http.MethodPost, "/yookassa/webhook", strings.NewReader(body))
	req.Header.Set("X-Custom-Sig", sign(secret, body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
}
