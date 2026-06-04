// Command scheduler runs the per-user digest cron. On start (and every
// reload tick thereafter) it loads each non-blocked user's digest schedule
// from user_settings.digest_schedule (jsonb) and registers gocron jobs that
// publish delivery.scheduled events. The processor's delivery consumer
// picks those up and runs the assembler.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/virsi/mute-bot/internal/config"
	"github.com/virsi/mute-bot/internal/obs"
	"github.com/virsi/mute-bot/internal/queue"
	"github.com/virsi/mute-bot/internal/scheduler"
	"github.com/virsi/mute-bot/internal/storage/postgres"
	"github.com/virsi/mute-bot/internal/users"
)

func main() {
	if err := run(); err != nil {
		slog.Error("scheduler: fatal", slog.Any("err", err))
		os.Exit(1)
	}
}

func run() error {
	cfgPath := flag.String("config", "configs/config.yaml", "path to config yaml")
	flag.Parse()

	slog.SetDefault(obs.NewLogger(slog.LevelInfo, "scheduler"))

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// Metrics endpoint on the scheduler slot. Cron job counters surface
	// here so Prometheus can alert on a scheduler that has stopped firing.
	metricsSrv := obs.ServeMetrics(":9104")
	defer func() { _ = metricsSrv.Close() }()

	rootCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	shutdownTracing, err := obs.SetupTracing(rootCtx, "scheduler", cfg.OTLPEndpoint)
	if err != nil {
		return fmt.Errorf("setup tracing: %w", err)
	}
	defer func() {
		shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelShutdown()
		_ = shutdownTracing(shutdownCtx)
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-stop
		slog.Info("scheduler: shutdown signal", slog.String("sig", sig.String()))
		cancel()
	}()

	pool, err := postgres.NewPool(rootCtx, cfg.PostgresDSN)
	if err != nil {
		return fmt.Errorf("pg pool: %w", err)
	}
	defer pool.Close()

	nc, err := queue.Connect(rootCtx, cfg.NATSURL)
	if err != nil {
		return fmt.Errorf("nats connect: %w", err)
	}
	defer nc.Close()
	if err := nc.EnsureStreams(rootCtx); err != nil {
		return fmt.Errorf("nats ensure streams: %w", err)
	}
	pub := queue.NewPublisher(nc)

	loader := newUsersLoader(pool)

	cron, err := scheduler.NewCron(scheduler.CronDeps{
		LoadUsers: loader,
		Publisher: pub,
	})
	if err != nil {
		return fmt.Errorf("new cron: %w", err)
	}
	if err := cron.Start(rootCtx); err != nil {
		return fmt.Errorf("cron start: %w", err)
	}

	// Hourly expiry sweep — runs alongside the cron so Pro users whose
	// tier_until has passed get downgraded back to free even when they
	// never touch the bot. Uses the users.Service Downgrader port.
	usersSvc := users.NewService(users.Deps{
		Users:    postgres.NewUsersRepo(pool),
		Settings: postgres.NewSettingsRepo(pool),
	})
	sweeper := scheduler.NewExpirySweeper(usersSvc, time.Hour, nil, slog.Default())
	go func() {
		if err := sweeper.Run(rootCtx); err != nil && !errors.Is(err, context.Canceled) {
			slog.Warn("scheduler: expiry sweeper exited", slog.Any("err", err))
		}
	}()

	slog.Info("scheduler: started")
	<-rootCtx.Done()
	if err := cron.Stop(); err != nil {
		slog.Warn("scheduler: stop error", slog.Any("err", err))
	}
	slog.Info("scheduler: stopped")
	return nil
}

// newUsersLoader returns the LoadUsersFunc that the cron calls on Start and
// on every reload tick. The query joins users and user_settings; the
// digest_schedule jsonb column is parsed inline into the cron's expected
// {times, tz} shape so the scheduler package stays free of schema knowledge.
func newUsersLoader(pool *postgres.Pool) scheduler.LoadUsersFunc {
	return func(ctx context.Context) ([]scheduler.UserSchedule, error) {
		const q = `
			SELECT u.id, u.tg_user_id, s.digest_schedule
			FROM users u
			JOIN user_settings s ON s.user_id = u.id
			WHERE u.blocked = false`
		rows, err := pool.Pool().Query(ctx, q)
		if err != nil {
			return nil, fmt.Errorf("query users: %w", err)
		}
		defer rows.Close()

		var out []scheduler.UserSchedule
		for rows.Next() {
			var (
				us        scheduler.UserSchedule
				schedJSON []byte
			)
			if err := rows.Scan(&us.UserID, &us.TGUserID, &schedJSON); err != nil {
				return nil, fmt.Errorf("scan user schedule: %w", err)
			}
			var parsed struct {
				Times []string `json:"times"`
				TZ    string   `json:"tz"`
			}
			// A malformed jsonb is logged and skipped — the row will be
			// retried on the next reload and meanwhile other users keep
			// firing on time.
			if err := json.Unmarshal(schedJSON, &parsed); err != nil {
				slog.Warn("scheduler: bad digest_schedule",
					slog.Int64("user_id", us.UserID),
					slog.Any("err", err))
				continue
			}
			us.Times, us.TZ = parsed.Times, parsed.TZ
			out = append(out, us)
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("rows err: %w", err)
		}
		return out, nil
	}
}
