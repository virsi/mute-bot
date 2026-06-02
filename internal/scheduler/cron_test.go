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
