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
type Cron struct {
	s     gocron.Scheduler
	d     CronDeps
	clock clockwork.Clock
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

	// Force scheduler-wide UTC: we convert each user's local HH:MM to
	// UTC HH:MM in scheduleUser, so gocron must interpret AtTimes in
	// UTC. Without this, gocron defaults to time.Local and the at-times
	// are mis-aligned by the system offset.
	s, err := gocron.NewScheduler(gocron.WithClock(clock), gocron.WithLocation(time.UTC))
	if err != nil {
		return nil, fmt.Errorf("new scheduler: %w", err)
	}
	return &Cron{s: s, d: d, clock: clock}, nil
}

// Start performs the initial user load, schedules per-user jobs, and
// launches the reload loop. The loop exits when ctx is cancelled.
// Start itself is non-blocking — gocron runs jobs on its own goroutines.
func (c *Cron) Start(ctx context.Context) error {
	if err := c.reload(ctx); err != nil {
		return err
	}
	c.s.Start()
	go c.reloadLoop(ctx)
	return nil
}

// Stop shuts down the underlying gocron scheduler. After Stop, the
// Cron cannot be restarted; construct a new one.
func (c *Cron) Stop() error {
	if err := c.s.Shutdown(); err != nil {
		return fmt.Errorf("shutdown: %w", err)
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
	for _, j := range c.s.Jobs() {
		_ = c.s.RemoveJob(j.ID())
	}
	for _, u := range users {
		c.scheduleUser(u)
	}
	return nil
}

// scheduleUser registers one gocron daily job per user time. Because
// gocron v2 has only a scheduler-wide WithLocation (no per-job option),
// we convert the user's local HH:MM to UTC HH:MM using the current
// offset and schedule daily at that UTC time. DST transitions can shift
// the user's local fire time by ±1 hour until the next reload re-evaluates
// the offset — acceptable for the MVP at 5-minute reload cadence.
func (c *Cron) scheduleUser(u UserSchedule) {
	loc, err := time.LoadLocation(u.TZ)
	if err != nil {
		loc = time.UTC
	}
	now := c.clock.Now().In(loc)
	for _, hhmm := range u.Times {
		hm, err := time.ParseInLocation("15:04", hhmm, loc)
		if err != nil {
			continue
		}
		// Build today's local instant at HH:MM, then convert to UTC to
		// get the correct UTC HH:MM under the user's current offset.
		local := time.Date(now.Year(), now.Month(), now.Day(), hm.Hour(), hm.Minute(), 0, 0, loc)
		utc := local.UTC()
		atTime := gocron.NewAtTime(uint(utc.Hour()), uint(utc.Minute()), 0)

		userID := u.UserID
		tgUserID := u.TGUserID
		_, err = c.s.NewJob(
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
