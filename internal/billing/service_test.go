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

// stubProvider is a hand-rolled fake Provider that returns prebuilt
// Activations and InvoiceURLs. Hand-rolled (vs testify/mock) so the test
// reads top-to-bottom.
type stubProvider struct {
	name     string
	url      string
	urlErr   error
	act      Activation
	actErr   error
	urlCalls int
	actCalls int
}

func (s *stubProvider) Name() string { return s.name }
func (s *stubProvider) InvoiceURL(_ context.Context, _ int64, _ string) (string, error) {
	s.urlCalls++
	return s.url, s.urlErr
}

func (s *stubProvider) HandlePayment(_ context.Context, _ []byte) (Activation, error) {
	s.actCalls++
	return s.act, s.actErr
}

// fakeSubs is an in-memory SubsRepo that mirrors the live ON CONFLICT
// behavior keyed by (provider, provider_ref).
type fakeSubs struct {
	mu     sync.Mutex
	nextID int64
	byRef  map[string]int64 // provider+":"+ref → id
	calls  int
	err    error
}

func newFakeSubs() *fakeSubs { return &fakeSubs{byRef: map[string]int64{}} }

func (f *fakeSubs) Insert(_ context.Context, in postgres.SubscriptionInsert) (int64, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return 0, false, f.err
	}
	key := in.Provider + ":" + in.ProviderRef
	if existing, ok := f.byRef[key]; ok {
		return existing, false, nil
	}
	f.nextID++
	f.byRef[key] = f.nextID
	return f.nextID, true, nil
}

// fakeUsers records GrantPro calls and resolves tg-id → fixed DB id.
type fakeUsers struct {
	mu              sync.Mutex
	resolveErr      error
	grantErr        error
	grantCalls      int
	registerCalls   int
	lastGrantUserID int64
	lastGrantDur    time.Duration
}

func (f *fakeUsers) RegisterOnStart(_ context.Context, tg int64, _ string) (postgres.User, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.registerCalls++
	if f.resolveErr != nil {
		return postgres.User{}, false, f.resolveErr
	}
	return postgres.User{ID: 1000 + tg, TGUserID: tg, Tier: "free"}, false, nil
}

func (f *fakeUsers) GrantPro(_ context.Context, id int64, dur time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.grantCalls++
	f.lastGrantUserID = id
	f.lastGrantDur = dur
	return f.grantErr
}

func newService(t *testing.T, p Provider, s SubsRepo, u Users) *Service {
	t.Helper()
	return NewService(Deps{Provider: p, Subs: s, Users: u, Now: func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }})
}

func TestService_CreateInvoice_DelegatesToProvider(t *testing.T) {
	p := &stubProvider{name: "tg_stars", url: "https://t.me/$abc"}
	svc := newService(t, p, newFakeSubs(), &fakeUsers{})
	url, err := svc.CreateInvoice(context.Background(), 12345, PlanPro30d)
	require.NoError(t, err)
	require.Equal(t, "https://t.me/$abc", url)
	require.Equal(t, 1, p.urlCalls)
}

func TestService_CreateInvoice_PropagatesProviderError(t *testing.T) {
	p := &stubProvider{urlErr: errors.New("upstream down")}
	svc := newService(t, p, newFakeSubs(), &fakeUsers{})
	_, err := svc.CreateInvoice(context.Background(), 1, PlanPro30d)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invoice url")
}

func TestService_Settle_NewWebhook_GrantsPro(t *testing.T) {
	p := &stubProvider{name: "tg_stars", act: Activation{
		UserID: 42, ProviderRef: "ref-1", Plan: PlanPro30d, Duration: Duration30d,
	}}
	subs := newFakeSubs()
	users := &fakeUsers{}
	svc := newService(t, p, subs, users)

	granted, err := svc.Settle(context.Background(), []byte("{}"))
	require.NoError(t, err)
	require.True(t, granted)
	require.Equal(t, 1, p.actCalls)
	require.Equal(t, 1, subs.calls)
	require.Equal(t, 1, users.grantCalls)
	require.Equal(t, int64(1042), users.lastGrantUserID, "resolves tg=42 → db=1042 via fakeUsers")
	require.Equal(t, Duration30d, users.lastGrantDur)
}

// TestService_Settle_DuplicateWebhook covers the load-bearing idempotency
// contract: the same payment delivered twice produces one subscription row
// and exactly one GrantPro call.
func TestService_Settle_DuplicateWebhook_NoSecondGrant(t *testing.T) {
	p := &stubProvider{name: "tg_stars", act: Activation{
		UserID: 42, ProviderRef: "ref-dup", Plan: PlanPro30d, Duration: Duration30d,
	}}
	subs := newFakeSubs()
	users := &fakeUsers{}
	svc := newService(t, p, subs, users)

	granted1, err := svc.Settle(context.Background(), []byte("{}"))
	require.NoError(t, err)
	require.True(t, granted1)

	granted2, err := svc.Settle(context.Background(), []byte("{}"))
	require.NoError(t, err)
	require.False(t, granted2, "second webhook with same ref must return granted=false")

	require.Equal(t, 1, users.grantCalls, "exactly one Pro grant despite two webhooks")
	require.Equal(t, 2, subs.calls, "Insert called twice — repo handles dedup, not service")
	require.Len(t, subs.byRef, 1, "exactly one row persisted")
}

func TestService_Settle_HandlePaymentError_NoSideEffects(t *testing.T) {
	p := &stubProvider{actErr: errors.New("bad payload")}
	subs := newFakeSubs()
	users := &fakeUsers{}
	svc := newService(t, p, subs, users)

	_, err := svc.Settle(context.Background(), []byte("{}"))
	require.Error(t, err)
	require.Equal(t, 0, subs.calls)
	require.Equal(t, 0, users.grantCalls)
}

func TestService_Settle_UserResolveError_NoPersist(t *testing.T) {
	p := &stubProvider{act: Activation{UserID: 42, ProviderRef: "x", Plan: PlanPro30d, Duration: Duration30d}}
	subs := newFakeSubs()
	users := &fakeUsers{resolveErr: errors.New("db down")}
	svc := newService(t, p, subs, users)

	_, err := svc.Settle(context.Background(), []byte("{}"))
	require.Error(t, err)
	require.Equal(t, 0, subs.calls)
	require.Equal(t, 0, users.grantCalls)
}

func TestService_Settle_SubsInsertError_NoGrant(t *testing.T) {
	p := &stubProvider{act: Activation{UserID: 42, ProviderRef: "x", Plan: PlanPro30d, Duration: Duration30d}}
	subs := newFakeSubs()
	subs.err = errors.New("constraint violation")
	users := &fakeUsers{}
	svc := newService(t, p, subs, users)

	_, err := svc.Settle(context.Background(), []byte("{}"))
	require.Error(t, err)
	require.Equal(t, 0, users.grantCalls)
}

func TestService_Settle_GrantProError_PropagatesAfterPersist(t *testing.T) {
	p := &stubProvider{name: "tg_stars", act: Activation{UserID: 42, ProviderRef: "x", Plan: PlanPro30d, Duration: Duration30d}}
	subs := newFakeSubs()
	users := &fakeUsers{grantErr: errors.New("db lock")}
	svc := newService(t, p, subs, users)

	granted, err := svc.Settle(context.Background(), []byte("{}"))
	require.Error(t, err)
	require.False(t, granted)
	// Subscription was already persisted; orchestrator returns the error so
	// a retried webhook will short-circuit at the conflict and NOT re-attempt
	// the grant. Pro will need an operator nudge — this is the simplest
	// failure mode we can ship safely.
	require.Equal(t, 1, subs.calls)
}
