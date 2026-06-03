package scheduler

import (
	"context"
	"log/slog"
	"time"

	"github.com/jonboulle/clockwork"
)

// defaultExpirySweepInterval is the gap between two sweeps. One hour is
// the natural granularity for tier_until expirations measured in days.
const defaultExpirySweepInterval = time.Hour

// Downgrader is the slice of users.Service the sweeper needs: bulk
// downgrade of every Pro user whose tier_until has passed, returning
// how many rows actually moved to free.
type Downgrader interface {
	DowngradeExpired(ctx context.Context) (int, error)
}

// ExpirySweeper periodically calls Downgrader.DowngradeExpired. It is
// hosted by cmd/scheduler as one of the long-running goroutines: a
// separate sweep keeps Pro membership in sync with the wall clock even
// when no traffic touches the affected users.
type ExpirySweeper struct {
	down     Downgrader
	interval time.Duration
	clock    clockwork.Clock
	logger   *slog.Logger
}

// NewExpirySweeper builds a sweeper. Zero/nil values fall through to
// safe defaults — one-hour interval, real wall-clock, slog.Default().
func NewExpirySweeper(d Downgrader, interval time.Duration, clock clockwork.Clock, l *slog.Logger) *ExpirySweeper {
	if interval == 0 {
		interval = defaultExpirySweepInterval
	}
	if clock == nil {
		clock = clockwork.NewRealClock()
	}
	if l == nil {
		l = slog.Default()
	}
	return &ExpirySweeper{down: d, interval: interval, clock: clock, logger: l}
}

// Run blocks until ctx is cancelled. Each tick logs the downgrade count
// at INFO when non-zero; a sweep error is logged at WARN and execution
// continues — the next tick retries.
func (s *ExpirySweeper) Run(ctx context.Context) error {
	t := s.clock.NewTicker(s.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.Chan():
			n, err := s.down.DowngradeExpired(ctx)
			if err != nil {
				s.logger.WarnContext(ctx, "expiry sweep failed", slog.Any("err", err))
				continue
			}
			if n > 0 {
				s.logger.InfoContext(ctx, "expiry sweep",
					slog.Int("downgraded", n))
			}
		}
	}
}
