package users

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/virsi/mute-bot/internal/storage/postgres"
)

// mockUsers stubs the UsersRW port via testify/mock so each test can
// program exactly the calls it expects.
type mockUsers struct{ mock.Mock }

func (m *mockUsers) GetOrCreate(ctx context.Context, tgID int64, username string) (postgres.User, bool, error) {
	args := m.Called(ctx, tgID, username)
	u, _ := args.Get(0).(postgres.User)
	return u, args.Bool(1), args.Error(2)
}

func (m *mockUsers) GrantPro(ctx context.Context, id int64, dur time.Duration) error {
	return m.Called(ctx, id, dur).Error(0)
}

func (m *mockUsers) BulkDowngradeExpired(ctx context.Context, asOf time.Time) ([]int64, error) {
	args := m.Called(ctx, asOf)
	ids, _ := args.Get(0).([]int64)
	return ids, args.Error(1)
}

// mockSettings stubs SettingsWriter.
type mockSettings struct{ mock.Mock }

func (m *mockSettings) Upsert(ctx context.Context, userID int64, in postgres.SettingsUpdate) error {
	return m.Called(ctx, userID, in).Error(0)
}

func TestRegisterOnStart_NewUser_SeedsSettings(t *testing.T) {
	u := postgres.User{ID: 42, TGUserID: 555, Username: "alice", Tier: "free"}
	mu := &mockUsers{}
	ms := &mockSettings{}
	mu.On("GetOrCreate", mock.Anything, int64(555), "alice").Return(u, true, nil)
	ms.On("Upsert", mock.Anything, int64(42), mock.MatchedBy(func(in postgres.SettingsUpdate) bool {
		// Seed must contain exactly the default topics + threshold + non-empty schedule.
		if len(in.Topics) != 2 || in.Topics[0] != "politics" || in.Topics[1] != "it" {
			return false
		}
		if in.Threshold != 50 || in.AlertThreshold != 85 || in.AlertsEnabled {
			return false
		}
		var parsed struct {
			Times []string `json:"times"`
			TZ    string   `json:"tz"`
		}
		if err := json.Unmarshal(in.ScheduleJSON, &parsed); err != nil {
			return false
		}
		return parsed.TZ == "Europe/Moscow" && len(parsed.Times) == 2
	})).Return(nil)

	s := NewService(Deps{Users: mu, Settings: ms})
	got, created, err := s.RegisterOnStart(context.Background(), 555, "alice")
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, int64(42), got.ID)
	mu.AssertExpectations(t)
	ms.AssertExpectations(t)
}

func TestRegisterOnStart_ExistingUser_NoSeed(t *testing.T) {
	u := postgres.User{ID: 42, TGUserID: 555, Username: "alice", Tier: "free"}
	mu := &mockUsers{}
	ms := &mockSettings{}
	mu.On("GetOrCreate", mock.Anything, int64(555), "alice").Return(u, false, nil)
	// ms.Upsert must NOT be called — assertion happens via AssertExpectations.

	s := NewService(Deps{Users: mu, Settings: ms})
	got, created, err := s.RegisterOnStart(context.Background(), 555, "alice")
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, int64(42), got.ID)
	mu.AssertExpectations(t)
	ms.AssertExpectations(t)
}

func TestRegisterOnStart_GetOrCreateError_Propagates(t *testing.T) {
	mu := &mockUsers{}
	ms := &mockSettings{}
	mu.On("GetOrCreate", mock.Anything, int64(555), "alice").
		Return(postgres.User{}, false, errors.New("boom"))

	s := NewService(Deps{Users: mu, Settings: ms})
	_, _, err := s.RegisterOnStart(context.Background(), 555, "alice")
	require.Error(t, err)
	require.Contains(t, err.Error(), "boom")
}

func TestIsPro_FreeUser(t *testing.T) {
	s := NewService(Deps{Users: &mockUsers{}, Settings: &mockSettings{}})
	require.False(t, s.IsPro(postgres.User{Tier: "free"}))
}

func TestIsPro_LifetimePro_NoTierUntil(t *testing.T) {
	s := NewService(Deps{Users: &mockUsers{}, Settings: &mockSettings{}})
	require.True(t, s.IsPro(postgres.User{Tier: "pro"}))
}

func TestIsPro_FutureTierUntil(t *testing.T) {
	now := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	future := now.Add(24 * time.Hour)
	s := NewService(Deps{
		Users: &mockUsers{}, Settings: &mockSettings{},
		Now: func() time.Time { return now },
	})
	require.True(t, s.IsPro(postgres.User{Tier: "pro", TierUntil: &future}))
}

func TestIsPro_PastTierUntil(t *testing.T) {
	now := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Hour)
	s := NewService(Deps{
		Users: &mockUsers{}, Settings: &mockSettings{},
		Now: func() time.Time { return now },
	})
	require.False(t, s.IsPro(postgres.User{Tier: "pro", TierUntil: &past}))
}

func TestGrantPro_DelegatesToRepo(t *testing.T) {
	mu := &mockUsers{}
	mu.On("GrantPro", mock.Anything, int64(42), 30*24*time.Hour).Return(nil)
	s := NewService(Deps{Users: mu, Settings: &mockSettings{}})
	require.NoError(t, s.GrantPro(context.Background(), 42, 30*24*time.Hour))
	mu.AssertExpectations(t)
}

func TestDowngradeExpired_ReturnsCountFromRepo(t *testing.T) {
	now := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	mu := &mockUsers{}
	mu.On("BulkDowngradeExpired", mock.Anything, now).Return([]int64{1, 2, 3}, nil)

	s := NewService(Deps{
		Users: mu, Settings: &mockSettings{},
		Now: func() time.Time { return now },
	})
	n, err := s.DowngradeExpired(context.Background())
	require.NoError(t, err)
	require.Equal(t, 3, n)
	mu.AssertExpectations(t)
}

func TestDowngradeExpired_RepoError_Surfaces(t *testing.T) {
	mu := &mockUsers{}
	mu.On("BulkDowngradeExpired", mock.Anything, mock.Anything).
		Return([]int64(nil), errors.New("pg down"))

	s := NewService(Deps{Users: mu, Settings: &mockSettings{}})
	n, err := s.DowngradeExpired(context.Background())
	require.Error(t, err)
	require.Equal(t, 0, n)
}
