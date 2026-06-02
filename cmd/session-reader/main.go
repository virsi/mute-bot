// Command session-reader is the MTProto user-session process. It connects to
// Telegram with a stored gotd session, subscribes to channel-message updates,
// extracts each post into a domain.RawPost, and publishes it onto the
// ingest.raw NATS subject for downstream normalization.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/gotd/td/tg"

	"github.com/virsi/mute-bot/internal/config"
	"github.com/virsi/mute-bot/internal/mtproto"
	"github.com/virsi/mute-bot/internal/obs"
	"github.com/virsi/mute-bot/internal/queue"
	"github.com/virsi/mute-bot/internal/storage/postgres"
)

func main() {
	if err := run(); err != nil {
		slog.Error("session-reader: fatal", slog.Any("err", err))
		os.Exit(1)
	}
}

func run() error {
	cfgPath := flag.String("config", "configs/config.yaml", "path to config yaml")
	flag.Parse()

	slog.SetDefault(obs.NewLogger(slog.LevelInfo, "session-reader"))

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// Metrics endpoint runs alongside the MTProto reader so Prometheus can
	// scrape ingest counters even when the reader goroutine is blocked on
	// Telegram I/O. Port 9101 is the session-reader slot in the obs scheme
	// (9102 processor, 9103 bot-api, 9104 scheduler).
	metricsSrv := obs.ServeMetrics(":9101")
	defer func() { _ = metricsSrv.Close() }()

	rootCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-stop
		slog.Info("session-reader: shutdown signal", slog.String("sig", sig.String()))
		cancel()
	}()

	pool, err := postgres.NewPool(rootCtx, cfg.PostgresDSN)
	if err != nil {
		return fmt.Errorf("pg pool: %w", err)
	}
	defer pool.Close()
	// session state repo is wired here to keep the dependency graph explicit;
	// catchup wiring happens once we have channel rows to back-fill against.
	_ = postgres.NewSessionStateRepo(pool)
	_ = postgres.NewChannelsRepo(pool)

	nc, err := queue.Connect(rootCtx, cfg.NATSURL)
	if err != nil {
		return fmt.Errorf("nats connect: %w", err)
	}
	defer nc.Close()
	if err := nc.EnsureStreams(rootCtx); err != nil {
		return fmt.Errorf("nats ensure streams: %w", err)
	}
	pub := queue.NewPublisher(nc)

	sessCfg := mtproto.SessionConfig{
		APIID:       cfg.MTProto.APIID,
		APIHash:     cfg.MTProto.APIHash,
		SessionPath: cfg.MTProto.SessionPath,
		Phone:       os.Getenv("MUTE_TG_PHONE"),
		Code: func(_ context.Context, _ *tg.AuthSentCode) (string, error) {
			return readLine("Enter Telegram login code: ")
		},
		Password: func(_ context.Context) (string, error) {
			return readLine("Enter 2FA password (empty if none): ")
		},
	}

	client, _, err := mtproto.NewClient(sessCfg)
	if err != nil {
		return fmt.Errorf("mtproto client: %w", err)
	}

	reader := mtproto.NewReader(client, pub)

	slog.Info("session-reader: starting", slog.String("session", cfg.MTProto.SessionPath))
	authFn := func(ctx context.Context) error {
		return mtproto.Authenticate(ctx, client, sessCfg)
	}
	if err := reader.Run(rootCtx, authFn); err != nil && rootCtx.Err() == nil {
		return fmt.Errorf("reader: %w", err)
	}
	slog.Info("session-reader: stopped")
	return nil
}

// readLine reads a single line from stdin. Used only by the local dev path
// when the session file is missing and a fresh login is required; production
// deployments should pre-seed the session and never hit this path.
func readLine(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	sc := bufio.NewScanner(os.Stdin)
	if !sc.Scan() {
		if err := sc.Err(); err != nil {
			return "", fmt.Errorf("read stdin: %w", err)
		}
		return "", fmt.Errorf("no input")
	}
	return strings.TrimSpace(sc.Text()), nil
}
