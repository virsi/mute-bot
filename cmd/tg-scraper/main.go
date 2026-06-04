// Command tg-scraper is the public-channel HTML ingest path. It polls
// https://t.me/s/<username> for each configured channel, parses the visible
// posts, and publishes any unseen ones onto NATS ingest.raw. Same downstream
// contract as cmd/session-reader; chosen when an MTProto session is not
// available.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/virsi/mute-bot/internal/config"
	"github.com/virsi/mute-bot/internal/obs"
	"github.com/virsi/mute-bot/internal/queue"
	"github.com/virsi/mute-bot/internal/storage/postgres"
	"github.com/virsi/mute-bot/internal/tgscraper"
)

type channelsFile struct {
	Channels []struct {
		Username       string `yaml:"username"`
		AuthorityScore int    `yaml:"authority_score"`
	} `yaml:"channels"`
}

func main() {
	if err := run(); err != nil {
		slog.Error("tg-scraper: fatal", slog.Any("err", err))
		os.Exit(1)
	}
}

func run() error {
	cfgPath := flag.String("config", "configs/config.yaml", "path to config yaml")
	pollSec := flag.Int("poll", 60, "polling interval, seconds")
	flag.Parse()

	slog.SetDefault(obs.NewLogger(slog.LevelInfo, "tg-scraper"))

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	metricsSrv := obs.ServeMetrics(":9101")
	defer func() { _ = metricsSrv.Close() }()

	rootCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-stop
		slog.Info("tg-scraper: shutdown signal", slog.String("sig", sig.String()))
		cancel()
	}()

	pool, err := postgres.NewPool(rootCtx, cfg.PostgresDSN)
	if err != nil {
		return fmt.Errorf("pg pool: %w", err)
	}
	defer pool.Close()
	channelsRepo := postgres.NewChannelsRepo(pool)
	sessRepo := postgres.NewSessionStateRepo(pool)

	specs, err := loadChannels(cfg.ChannelsFile)
	if err != nil {
		return fmt.Errorf("load channels: %w", err)
	}
	if len(specs) == 0 {
		return fmt.Errorf("no channels configured in %s", cfg.ChannelsFile)
	}

	internalIDs := make(map[string]int64, len(specs))
	for _, s := range specs {
		tgID := tgscraper.PseudoChannelID(s.Username)
		id, err := channelsRepo.Upsert(rootCtx, postgres.ChannelInsert{
			TGChannelID: tgID,
			Username:    s.Username,
			Title:       s.Username,
			Authority:   s.Authority,
		})
		if err != nil {
			return fmt.Errorf("upsert channel %s: %w", s.Username, err)
		}
		internalIDs[s.Username] = id
		slog.Info("tg-scraper: channel registered",
			slog.String("username", s.Username),
			slog.Int64("internal_id", id),
			slog.Int64("pseudo_tg_id", tgID),
		)
	}

	nc, err := queue.Connect(rootCtx, cfg.NATSURL)
	if err != nil {
		return fmt.Errorf("nats connect: %w", err)
	}
	defer nc.Close()
	if err := nc.EnsureStreams(rootCtx); err != nil {
		return fmt.Errorf("nats ensure streams: %w", err)
	}
	pub := queue.NewPublisher(nc)

	w := tgscraper.NewWorker(
		specs,
		tgscraper.Config{
			PollInterval: time.Duration(*pollSec) * time.Second,
		},
		pub,
		sessRepo,
		internalIDs,
	)

	slog.Info("tg-scraper: starting",
		slog.Int("channels", len(specs)),
		slog.Int("poll_sec", *pollSec),
	)
	if err := w.Run(rootCtx); err != nil && rootCtx.Err() == nil {
		return fmt.Errorf("worker: %w", err)
	}
	slog.Info("tg-scraper: stopped")
	return nil
}

func loadChannels(path string) ([]tgscraper.ChannelSpec, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read channels file: %w", err)
	}
	var f channelsFile
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("parse channels yaml: %w", err)
	}
	out := make([]tgscraper.ChannelSpec, 0, len(f.Channels))
	for _, c := range f.Channels {
		if c.Username == "" {
			continue
		}
		auth := c.AuthorityScore
		if auth == 0 {
			auth = 50
		}
		out = append(out, tgscraper.ChannelSpec{
			Username:  c.Username,
			Authority: auth,
		})
	}
	return out, nil
}
