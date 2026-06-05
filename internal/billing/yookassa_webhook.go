package billing

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
)

// SettlerForWebhook is the subset of billing.Service the webhook needs.
// Same shape as bot.Settler but redeclared here so the billing package
// stays import-free of internal/bot.
type SettlerForWebhook interface {
	Settle(ctx context.Context, provider string, raw []byte) (bool, error)
}

// YooKassaWebhookDeps configures NewYooKassaWebhook.
type YooKassaWebhookDeps struct {
	// Settler dispatches the validated body into billing.Service.
	Settler SettlerForWebhook
	// Secret is the HMAC SHA256 key shared with YooKassa. Compared to
	// the value in the SignatureHeader via hmac.Equal.
	Secret string
	// SignatureHeader overrides the default "X-YooKassa-Signature" name.
	// Operators can switch headers without code changes when YooKassa
	// changes the public header.
	SignatureHeader string
	// MaxBodyBytes caps request body size to defend against malicious
	// unbounded reads. Defaults to 64 KiB.
	MaxBodyBytes int64
	// Logger overrides slog.Default. Optional.
	Logger *slog.Logger
}

// YooKassaWebhook is the http.Handler hosted at /yookassa/webhook by the
// bot-api process. It HMAC-verifies the body, delegates to billing.Service
// for idempotent activation, and maps Settle results onto HTTP status:
//
//   - 200 OK on grant or duplicate
//   - 401 Unauthorized on bad/missing signature
//   - 500 Internal Server Error on settler/parser failures (YooKassa
//     retries on 5xx, which is the desired behaviour for transient bugs).
type YooKassaWebhook struct {
	settler   SettlerForWebhook
	secret    []byte
	sigHeader string
	maxBytes  int64
	logger    *slog.Logger
}

// NewYooKassaWebhook constructs a YooKassaWebhook with sensible defaults.
func NewYooKassaWebhook(d YooKassaWebhookDeps) *YooKassaWebhook {
	if d.SignatureHeader == "" {
		d.SignatureHeader = "X-YooKassa-Signature"
	}
	if d.MaxBodyBytes == 0 {
		d.MaxBodyBytes = 64 * 1024
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	return &YooKassaWebhook{
		settler:   d.Settler,
		secret:    []byte(d.Secret),
		sigHeader: d.SignatureHeader,
		maxBytes:  d.MaxBodyBytes,
		logger:    d.Logger,
	}
}

// ServeHTTP implements http.Handler.
func (h *YooKassaWebhook) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	sig := r.Header.Get(h.sigHeader)
	if sig == "" {
		h.logger.WarnContext(r.Context(), "yookassa: missing signature")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, h.maxBytes))
	if err != nil {
		h.logger.WarnContext(r.Context(), "yookassa: read body", slog.Any("err", err))
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if !verifyHMAC(h.secret, body, sig) {
		h.logger.WarnContext(r.Context(), "yookassa: signature mismatch",
			slog.Int("body_len", len(body)))
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	granted, err := h.settler.Settle(r.Context(), "yookassa", body)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "yookassa: settle", slog.Any("err", err))
		http.Error(w, "settle failed", http.StatusInternalServerError)
		return
	}
	if granted {
		h.logger.InfoContext(r.Context(), "yookassa: pro granted")
	} else {
		h.logger.InfoContext(r.Context(), "yookassa: duplicate webhook")
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// verifyHMAC returns true iff the hex-encoded signature equals
// HMAC-SHA256(secret, body) in constant time. Defensive against
// timing-side-channel attacks on the secret.
func verifyHMAC(secret, body []byte, sigHex string) bool {
	want, err := hex.DecodeString(sigHex)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	got := mac.Sum(nil)
	return hmac.Equal(want, got)
}
