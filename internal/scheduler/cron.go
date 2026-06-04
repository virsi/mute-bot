package scheduler

import (
	"context"
	"fmt"
	"time"

	"github.com/go-co-op/gocron/v2"
	"github.com/jonboulle/clockwork"

	"github.com/virsi/mute-bot/internal/queue"
)

// UserSchedule is the per-user fan-out unit the cron consumes: which
// user gets a digest at which local times. Times are "HH:MM" strings
// interpreted in the named TZ (IANA name like "Europe/Moscow"). Invalid
// timezone falls back to UTC.
type UserSchedule struct {
	UserID   int64
	TGUserID int64
	Times    []string
	TZ       string
}

// LoadUsersFunc is the source of truth for the cron's user list. It is
// called on Start and then periodically by the reload loop so newly
// registered users pick up their schedule without a process restart.
type LoadUsersFunc func(ctx context.Context) ([]UserSchedule, error)

// Publisher is the minimal queue surface the cron needs. It is satisfied
// by *queue.Publisher in production; tests inject a func adapter to
// capture calls.
type Publisher interface {
	Publish(ctx context.Context, subject string, payload any) error
}

// CronDeps groups the cron's collaborators. Clock is optional — when
// nil, the cron uses a real wall-clock; tests inject a fake to drive
// time deterministically. Reload defaults to 5 minutes when zero.
type CronDeps struct {
	LoadUsers LoadUsersFunc
	Publisher Publisher
	Reload    time.Duration
	Clock     clockwork.Clock
}

// Cron drives per-user digest jobs. Each user's HH:MM times become a
// gocron DailyJob that publishes a delivery.scheduled message. The cron
// periodically reloads the user list to pick up changes from settings.
//
// gocron v2 only supports a scheduler-wide location, not a per-job one,
// so we keep one Scheduler instance per IANA timezone. That way DST
// transitions are handled correctly by gocron itself — a user in
// Europe/Berlin keeps firing at local 09:00 across the March/October
// jumps without any wall-clock conversion in this package.
type Cron struct {
	d     CronDeps
	clock clockwork.Clock
	// schedulers is keyed by IANA timezone string; we lazily build one
	// gocron.Scheduler per location seen in the user list.
	schedulers map[string]gocron.Scheduler
}

// NewCron constructs a Cron. The scheduler is not started yet — call
// Start to begin firing jobs. Returns an error if gocron fails to
// initialize its internal scheduler.
func NewCron(d CronDeps) (*Cron, error) {
	if d.LoadUsers == nil {
		return nil, fmt.Errorf("LoadUsers is required")
	}
	if d.Publisher == nil {
		return nil, fmt.Errorf("Publisher is required")
	}
	if d.Reload == 0 {
		d.Reload = 5 * time.Minute
	}
	clock := d.Clock
	if clock == nil {
		clock = clockwork.NewRealClock()
	}

	return &Cron{d: d, clock: clock, schedulers: make(map[string]gocron.Scheduler)}, nil
}

// schedulerFor returns the gocron.Scheduler bound to loc, creating one on
// first use. The scheduler is auto-started so jobs registered against it
// after the initial Start() still fire — reload may discover a new TZ.
func (c *Cron) schedulerFor(loc *time.Location) (gocron.Scheduler, error) {
	key := loc.String()
	if s, ok := c.schedulers[key]; ok {
		return s, nil
	}
	s, err := gocron.NewScheduler(gocron.WithClock(c.clock), gocron.WithLocation(loc))
	if err != nil {
		return nil, fmt.Errorf("new scheduler: %w", err)
	}
	c.schedulers[key] = s
	// Newly-created schedulers must be started right away; the top-level
	// Start() iterates the map once before reload returns. Calling Start
	// again on an already-running scheduler is a noop per gocron docs.
	s.Start()
	return s, nil
}

// Start performs the initial user load, schedules per-user jobs, and
// launches the reload loop. The loop exits when ctx is cancelled.
// Start itself is non-blocking — gocron runs jobs on its own goroutines.
func (c *Cron) Start(ctx context.Context) error {
	if err := c.reload(ctx); err != nil {
		return err
	}
	for _, s := range c.schedulers {
		s.Start()
	}
	go c.reloadLoop(ctx)
	return nil
}

// Stop shuts down every per-timezone scheduler. After Stop, the Cron
// cannot be restarted; construct a new one.
func (c *Cron) Stop() error {
	for _, s := range c.schedulers {
		if err := s.Shutdown(); err != nil {
			return fmt.Errorf("shutdown: %w", err)
		}
	}
	return nil
}

func (c *Cron) reloadLoop(ctx context.Context) {
	t := c.clock.NewTicker(c.d.Reload)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.Chan():
			_ = c.reload(ctx)
		}
	}
}

// reload clears existing jobs and re-registers them from the current
// user list. We rebuild from scratch rather than diff because the job
// count is small (≤ users × times-per-user) and the simplicity of
// "drop and re-add" beats incremental diff bookkeeping.
func (c *Cron) reload(ctx context.Context) error {
	users, err := c.d.LoadUsers(ctx)
	if err != nil {
		return fmt.Errorf("load users: %w", err)
	}
	for _, s := range c.schedulers {
		for _, j := range s.Jobs() {
			_ = s.RemoveJob(j.ID())
		}
	}
	for _, u := range users {
		c.scheduleUser(u)
	}
	return nil
}

// scheduleUser registers one gocron daily job per user time. Jobs are
// attached to the per-timezone scheduler returned by schedulerFor, so
// gocron interprets the at-time in the user's local TZ and follows DST
// transitions automatically (no manual ±1h compensation here).
func (c *Cron) scheduleUser(u UserSchedule) {
	loc, err := time.LoadLocation(u.TZ)
	if err != nil {
		loc = time.UTC
	}
	s, err := c.schedulerFor(loc)
	if err != nil {
		return
	}
	for _, hhmm := range u.Times {
		hm, err := time.Parse("15:04", hhmm)
		if err != nil {
			continue
		}
		// #nosec G115 -- hm.Hour()/Minute() are bounded by the time package
		atTime := gocron.NewAtTime(uint(hm.Hour()), uint(hm.Minute()), 0)

		userID := u.UserID
		tgUserID := u.TGUserID
		_, err = s.NewJob(
			gocron.DailyJob(1, gocron.NewAtTimes(atTime)),
			gocron.NewTask(func() {
				_ = c.d.Publisher.Publish(context.Background(), queue.SubjectDeliverySched,
					map[string]any{
						"user_id":    userID,
						"tg_user_id": tgUserID,
						"channel":    "digest",
					})
			}),
		)
		if err != nil {
			continue
		}
	}
}
