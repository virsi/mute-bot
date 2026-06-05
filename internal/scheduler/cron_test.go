package scheduler

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/require"

	"github.com/virsi/mute-bot/internal/queue"
)

// pubFunc adapts a function to the Publisher interface so tests can capture
// publish calls without wiring real NATS.
type pubFunc func(ctx context.Context, subject string, payload any) error

func (f pubFunc) Publish(ctx context.Context, subject string, payload any) error {
	return f(ctx, subject, payload)
}

// TestCron_FiresPerUser drives the cron with a fake clock so the test is
// deterministic regardless of wall-clock time. The user is scheduled at
// 10:00 and the clock starts at 09:59:30 — advancing 31 seconds crosses
// the at-time and triggers the job.
func TestCron_FiresPerUser(t *testing.T) {
	t.Parallel()

	fake := clockwork.NewFakeClockAt(time.Date(2026, 6, 3, 9, 59, 30, 0, time.UTC))

	type captured struct {
		subject string
		payload any
	}
	var (
		mu    sync.Mutex
		calls []captured
		fired int32
	)
	publish := pubFunc(func(_ context.Context, subject string, payload any) error {
		mu.Lock()
		calls = append(calls, captured{subject: subject, payload: payload})
		mu.Unlock()
		atomic.AddInt32(&fired, 1)
		return nil
	})

	loader := func(_ context.Context) ([]UserSchedule, error) {
		return []UserSchedule{
			{UserID: 1, TGUserID: 100, Times: []string{"10:00"}, TZ: "UTC"},
		}, nil
	}

	c, err := NewCron(CronDeps{
		LoadUsers: loader,
		Publisher: publish,
		Clock:     fake,
	})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, c.Start(ctx))
	t.Cleanup(func() { _ = c.Stop() })

	// Advance past 10:00:00 so the daily-at-time fires.
	fake.Advance(45 * time.Second)

	require.Eventually(t, func() bool { return atomic.LoadInt32(&fired) >= 1 },
		3*time.Second, 25*time.Millisecond,
		"job should have fired after clock advanced past at-time")

	mu.Lock()
	defer mu.Unlock()
	require.GreaterOrEqual(t, len(calls), 1)
	require.Equal(t, queue.SubjectDeliverySched, calls[0].subject)

	payload, ok := calls[0].payload.(map[string]any)
	require.True(t, ok, "payload should be map[string]any")
	require.Equal(t, int64(1), payload["user_id"])
	require.Equal(t, int64(100), payload["tg_user_id"])
	require.Equal(t, "digest", payload["channel"])
}

// TestCron_PerLocationScheduler asserts that NewCron lazily builds one
// gocron.Scheduler per distinct user timezone. This is the structural
// guarantee that fixes the DST drift bug: each scheduler is constructed
// with gocron.WithLocation(loc) so daily at-times are interpreted in the
// user's local TZ and gocron handles the spring-forward / fall-back
// boundaries automatically.
func TestCron_PerLocationScheduler(t *testing.T) {
	t.Parallel()

	fake := clockwork.NewFakeClockAt(time.Date(2026, 6, 3, 0, 0, 0, 0, time.UTC))

	publish := pubFunc(func(_ context.Context, _ string, _ any) error { return nil })
	loader := func(_ context.Context) ([]UserSchedule, error) {
		return []UserSchedule{
			{UserID: 1, TGUserID: 100, Times: []string{"09:00"}, TZ: "Europe/Berlin"},
			{UserID: 2, TGUserID: 200, Times: []string{"09:00"}, TZ: "Europe/Moscow"},
			{UserID: 3, TGUserID: 300, Times: []string{"09:00"}, TZ: "Europe/Berlin"},
			{UserID: 4, TGUserID: 400, Times: []string{"09:00"}, TZ: "UTC"},
		}, nil
	}

	c, err := NewCron(CronDeps{
		LoadUsers: loader,
		Publisher: publish,
		Clock:     fake,
		Reload:    time.Hour,
	})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, c.Start(ctx))
	t.Cleanup(func() { _ = c.Stop() })

	// 3 distinct TZs ⇒ 3 schedulers. Berlin users share one.
	c.mu.Lock()
	defer c.mu.Unlock()
	require.Len(t, c.schedulers, 3)
	require.Contains(t, c.schedulers, "Europe/Berlin")
	require.Contains(t, c.schedulers, "Europe/Moscow")
	require.Contains(t, c.schedulers, "UTC")
}

// TestCron_WeeklyJob_FiresSunday18Local advances a fake clock across Sunday
// 18:00 in the user's TZ and asserts the weekly subject was published. The
// daily 08:00 job in the same schedule must not interfere — the clock only
// crosses the weekly at-time.
func TestCron_WeeklyJob_FiresSunday18Local(t *testing.T) {
	t.Parallel()

	moscow, err := time.LoadLocation("Europe/Moscow")
	require.NoError(t, err)
	// Sunday 7 June 2026, 17:59 MSK — one minute before the weekly fires.
	start := time.Date(2026, 6, 7, 17, 59, 0, 0, moscow)
	fake := clockwork.NewFakeClockAt(start)

	type captured struct {
		subject string
		payload any
	}
	var (
		mu    sync.Mutex
		calls []captured
	)
	publish := pubFunc(func(_ context.Context, subject string, payload any) error {
		mu.Lock()
		calls = append(calls, captured{subject: subject, payload: payload})
		mu.Unlock()
		return nil
	})

	c, err := NewCron(CronDeps{
		LoadUsers: func(_ context.Context) ([]UserSchedule, error) {
			return []UserSchedule{{
				UserID: 1, TGUserID: 100,
				Times: []string{"08:00"}, TZ: "Europe/Moscow",
				WeeklyEnabled: true,
			}}, nil
		},
		Publisher: publish, Clock: fake, Reload: time.Hour,
	})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, c.Start(ctx))
	t.Cleanup(func() { _ = c.Stop() })

	// Cross 18:00 MSK by advancing two minutes.
	fake.Advance(2 * time.Minute)

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		for _, x := range calls {
			if x.subject == queue.SubjectDeliveryWeeklySched {
				return true
			}
		}
		return false
	}, 3*time.Second, 25*time.Millisecond,
		"weekly job should have published on Sunday 18:00 local")

	mu.Lock()
	defer mu.Unlock()
	// Find the weekly call and assert payload shape.
	var found bool
	for _, x := range calls {
		if x.subject != queue.SubjectDeliveryWeeklySched {
			continue
		}
		p, ok := x.payload.(map[string]any)
		require.True(t, ok)
		require.Equal(t, int64(1), p["user_id"])
		require.Equal(t, int64(100), p["tg_user_id"])
		found = true
	}
	require.True(t, found)
}

// TestCron_WeeklyJob_NoJobWhenDisabled asserts that weekly_enabled=false
// users never get a weekly job registered, even when the clock crosses
// Sunday 18:00.
func TestCron_WeeklyJob_NoJobWhenDisabled(t *testing.T) {
	t.Parallel()

	moscow, err := time.LoadLocation("Europe/Moscow")
	require.NoError(t, err)
	start := time.Date(2026, 6, 7, 17, 59, 0, 0, moscow)
	fake := clockwork.NewFakeClockAt(start)

	var (
		mu       sync.Mutex
		gotSubjs []string
	)
	publish := pubFunc(func(_ context.Context, subject string, _ any) error {
		mu.Lock()
		gotSubjs = append(gotSubjs, subject)
		mu.Unlock()
		return nil
	})

	c, err := NewCron(CronDeps{
		LoadUsers: func(_ context.Context) ([]UserSchedule, error) {
			return []UserSchedule{{
				UserID: 1, TGUserID: 100,
				Times: []string{"08:00"}, TZ: "Europe/Moscow",
				WeeklyEnabled: false,
			}}, nil
		},
		Publisher: publish, Clock: fake, Reload: time.Hour,
	})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, c.Start(ctx))
	t.Cleanup(func() { _ = c.Stop() })

	fake.Advance(2 * time.Minute)
	// Give gocron a moment to (not) fire.
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	for _, s := range gotSubjs {
		require.NotEqual(t, queue.SubjectDeliveryWeeklySched, s,
			"weekly subject must not be published when WeeklyEnabled=false")
	}
}
