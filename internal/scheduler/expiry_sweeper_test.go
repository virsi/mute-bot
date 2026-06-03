package scheduler

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/require"
)

// fakeDowngrader counts calls and returns a configured (n, err) tuple.
type fakeDowngrader struct {
	mu    sync.Mutex
	calls int32
	n     int
	err   error
}

func (f *fakeDowngrader) DowngradeExpired(_ context.Context) (int, error) {
	atomic.AddInt32(&f.calls, 1)
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.n, f.err
}

func (f *fakeDowngrader) Calls() int { return int(atomic.LoadInt32(&f.calls)) }

func TestExpirySweeper_FiresOnEveryTick(t *testing.T) {
	t.Parallel()

	fake := clockwork.NewFakeClockAt(time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC))
	d := &fakeDowngrader{n: 2}
	s := NewExpirySweeper(d, time.Hour, fake, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		_ = s.Run(ctx)
		close(done)
	}()

	// Wait until the ticker is registered, then advance the fake clock
	// one tick at a time, blocking on each delivery before stepping
	// forward. clockwork's ticker is buffered with capacity 1, so two
	// rapid Advance calls would coalesce into one delivery.
	require.NoError(t, fake.BlockUntilContext(ctx, 1))
	for i := 1; i <= 3; i++ {
		fake.Advance(time.Hour)
		require.Eventually(t, func() bool { return d.Calls() >= i },
			time.Second, 5*time.Millisecond,
			"after %d advances expected %d calls, got %d", i, i, d.Calls())
	}

	cancel()
	<-done
}

func TestExpirySweeper_DowngradeError_DoesNotStop(t *testing.T) {
	t.Parallel()

	fake := clockwork.NewFakeClockAt(time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC))
	d := &fakeDowngrader{err: errors.New("transient db blip")}
	s := NewExpirySweeper(d, time.Hour, fake, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		_ = s.Run(ctx)
		close(done)
	}()

	require.NoError(t, fake.BlockUntilContext(ctx, 1))
	fake.Advance(time.Hour)
	require.Eventually(t, func() bool { return d.Calls() >= 1 },
		1*time.Second, 10*time.Millisecond)

	// A subsequent tick still fires despite the prior error.
	fake.Advance(time.Hour)
	require.Eventually(t, func() bool { return d.Calls() >= 2 },
		1*time.Second, 10*time.Millisecond)

	cancel()
	<-done
}

func TestExpirySweeper_DefaultsApplyWhenZero(t *testing.T) {
	t.Parallel()

	s := NewExpirySweeper(&fakeDowngrader{}, 0, nil, nil)
	require.Equal(t, time.Hour, s.interval, "zero interval must default to one hour")
	require.NotNil(t, s.clock)
	require.NotNil(t, s.logger)
}

func TestExpirySweeper_CtxCancelExits(t *testing.T) {
	t.Parallel()

	fake := clockwork.NewFakeClockAt(time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC))
	s := NewExpirySweeper(&fakeDowngrader{}, time.Hour, fake, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("Run did not exit after cancel")
	}
}
