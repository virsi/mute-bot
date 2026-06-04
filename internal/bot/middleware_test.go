package bot

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/virsi/mute-bot/internal/storage/postgres"
)

// fakeRegistrar stubs the Registrar port. Tests parameterise the user
// returned by RegisterOnStart so each case can simulate Free / Pro /
// expired-Pro independently.
type fakeRegistrar struct {
	user postgres.User
	err  error
}

func (f *fakeRegistrar) RegisterOnStart(_ context.Context, tgID int64, username string) (postgres.User, bool, error) {
	u := f.user
	u.TGUserID = tgID
	u.Username = username
	return u, false, f.err
}

// fakeTier evaluates IsPro by the simple rule: tier == "pro" AND either
// no deadline or deadline strictly in the future according to now().
type fakeTier struct{ now time.Time }

func (f *fakeTier) IsPro(u postgres.User) bool {
	if u.Tier != "pro" {
		return false
	}
	if u.TierUntil == nil {
		return true
	}
	return u.TierUntil.After(f.now)
}

func TestRequirePro_AllowsProUser_NoDeadline(t *testing.T) {
	calls := 0
	next := func(_ context.Context, _ int64, _ string) error {
		calls++
		return nil
	}
	reg := &fakeRegistrar{user: postgres.User{ID: 1, Tier: "pro"}}
	tier := &fakeTier{now: time.Now()}
	send := &capturedSender{}

	gated := RequirePro(reg, tier, send, next)
	require.NoError(t, gated(context.Background(), 555, "alice"))
	require.Equal(t, 1, calls)
	require.Empty(t, send.msgs, "pro user must not see gate message")
}

func TestRequirePro_AllowsProUser_FutureDeadline(t *testing.T) {
	now := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	future := now.Add(24 * time.Hour)
	calls := 0
	next := func(_ context.Context, _ int64, _ string) error {
		calls++
		return nil
	}
	reg := &fakeRegistrar{user: postgres.User{ID: 1, Tier: "pro", TierUntil: &future}}
	tier := &fakeTier{now: now}
	send := &capturedSender{}

	gated := RequirePro(reg, tier, send, next)
	require.NoError(t, gated(context.Background(), 555, "alice"))
	require.Equal(t, 1, calls)
	require.Empty(t, send.msgs)
}

func TestRequirePro_BlocksFreeUser(t *testing.T) {
	calls := 0
	next := func(_ context.Context, _ int64, _ string) error {
		calls++
		return nil
	}
	reg := &fakeRegistrar{user: postgres.User{ID: 1, Tier: "free"}}
	tier := &fakeTier{now: time.Now()}
	send := &capturedSender{}

	gated := RequirePro(reg, tier, send, next)
	require.NoError(t, gated(context.Background(), 555, "alice"))
	require.Equal(t, 0, calls, "free user must not reach next handler")
	require.Len(t, send.msgs, 1)
	require.Contains(t, send.msgs[0], "/buy")
}

func TestRequirePro_BlocksExpiredProUser(t *testing.T) {
	now := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Hour)
	calls := 0
	next := func(_ context.Context, _ int64, _ string) error {
		calls++
		return nil
	}
	reg := &fakeRegistrar{user: postgres.User{ID: 1, Tier: "pro", TierUntil: &past}}
	tier := &fakeTier{now: now}
	send := &capturedSender{}

	gated := RequirePro(reg, tier, send, next)
	require.NoError(t, gated(context.Background(), 555, "alice"))
	require.Equal(t, 0, calls, "expired pro user must be treated as free")
	require.Len(t, send.msgs, 1)
}

func TestRequirePro_RegistrarError_Propagates(t *testing.T) {
	calls := 0
	next := func(_ context.Context, _ int64, _ string) error {
		calls++
		return nil
	}
	reg := &fakeRegistrar{err: errors.New("pg down")}
	tier := &fakeTier{now: time.Now()}
	send := &capturedSender{}

	gated := RequirePro(reg, tier, send, next)
	err := gated(context.Background(), 555, "alice")
	require.Error(t, err)
	require.Equal(t, 0, calls)
	require.Empty(t, send.msgs)
}
