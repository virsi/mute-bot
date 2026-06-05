package billing

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/virsi/mute-bot/internal/storage/postgres"
)

// fakeRenewer captures Renew invocations for assertions.
type fakeRenewer struct {
	mu        sync.Mutex
	calls     []renewerCall
	paymentID string
	err       error
}

type renewerCall struct {
	userID int64
	pmID   string
}

func (f *fakeRenewer) Renew(_ context.Context, userID int64, pmID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, renewerCall{userID, pmID})
	return f.paymentID, f.err
}

// fakeSubsExpiring stubs SubsExpiringReader. Returns the configured rows
// verbatim; useful for asserting Step's per-row behavior.
type fakeSubsExpiring struct {
	rows []postgres.ExpiringSubscription
	err  error
}

func (f *fakeSubsExpiring) ListExpiring(_ context.Context, _ time.Duration) ([]postgres.ExpiringSubscription, error) {
	return f.rows, f.err
}

func TestYooKassaRenewer_Step_TriggersRenewForYooKassaOnly(t *testing.T) {
	fr := &fakeRenewer{paymentID: "pay-x"}
	fs := &fakeSubsExpiring{rows: []postgres.ExpiringSubscription{
		{UserID: 1, TGUserID: 100, Provider: "yookassa", PaymentMethodID: "pm-1", ExpiresAt: time.Now().Add(1 * time.Hour)},
		// Stars rows MUST be skipped — payment_method_id is NULL anyway,
		// but the provider filter is the load-bearing guard.
		{UserID: 2, TGUserID: 200, Provider: "tg_stars", PaymentMethodID: "", ExpiresAt: time.Now().Add(1 * time.Hour)},
	}}
	r := NewYooKassaRenewer(YooKassaRenewerDeps{Renewer: fr, Subs: fs})
	require.NoError(t, r.Step(context.Background()))
	require.Len(t, fr.calls, 1)
	require.Equal(t, int64(100), fr.calls[0].userID)
	require.Equal(t, "pm-1", fr.calls[0].pmID)
}

// flakyRenewer fails for "pm-bad" and succeeds for the rest. Used to
// confirm Step does not bail out after a single failure.
type flakyRenewer struct {
	mu       sync.Mutex
	attempts int
}

func (f *flakyRenewer) Renew(_ context.Context, _ int64, pmID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attempts++
	if pmID == "pm-bad" {
		return "", errors.New("declined")
	}
	return "pay-ok", nil
}

func TestYooKassaRenewer_Step_ContinuesPastFailure(t *testing.T) {
	fr := &flakyRenewer{}
	fs := &fakeSubsExpiring{rows: []postgres.ExpiringSubscription{
		{UserID: 1, TGUserID: 100, Provider: "yookassa", PaymentMethodID: "pm-bad"},
		{UserID: 2, TGUserID: 200, Provider: "yookassa", PaymentMethodID: "pm-good"},
	}}
	r := NewYooKassaRenewer(YooKassaRenewerDeps{Renewer: fr, Subs: fs})
	require.NoError(t, r.Step(context.Background()))
	require.Equal(t, 2, fr.attempts, "both rows attempted despite first failure")
}

func TestYooKassaRenewer_Step_EmptyList(t *testing.T) {
	fr := &fakeRenewer{paymentID: "pay-x"}
	fs := &fakeSubsExpiring{rows: nil}
	r := NewYooKassaRenewer(YooKassaRenewerDeps{Renewer: fr, Subs: fs})
	require.NoError(t, r.Step(context.Background()))
	require.Empty(t, fr.calls)
}

func TestYooKassaRenewer_Step_ListErrorPropagates(t *testing.T) {
	fr := &fakeRenewer{}
	fs := &fakeSubsExpiring{err: errors.New("db down")}
	r := NewYooKassaRenewer(YooKassaRenewerDeps{Renewer: fr, Subs: fs})
	err := r.Step(context.Background())
	require.Error(t, err)
	require.Empty(t, fr.calls)
}

func TestYooKassaRenewer_Run_ContextCancelStops(t *testing.T) {
	fr := &fakeRenewer{paymentID: "pay-x"}
	fs := &fakeSubsExpiring{rows: []postgres.ExpiringSubscription{
		{UserID: 1, TGUserID: 100, Provider: "yookassa", PaymentMethodID: "pm-1"},
	}}
	r := NewYooKassaRenewer(YooKassaRenewerDeps{
		Renewer: fr, Subs: fs,
		Interval: 50 * time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	// Allow the initial Step to run.
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(1 * time.Second):
		t.Fatal("Run did not exit after cancel")
	}
	require.GreaterOrEqual(t, len(fr.calls), 1, "initial Step should run before cancel")
}
