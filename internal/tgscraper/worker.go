package tgscraper

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/virsi/mute-bot/internal/queue"
)

// ChannelSpec describes one channel the worker should poll.
type ChannelSpec struct {
	Username  string
	Authority int
}

// Publisher is the narrow surface used by the worker to emit raw posts; same
// shape as the MTProto reader uses, so tests can stub it without NATS.
type Publisher interface {
	Publish(ctx context.Context, subject string, payload any) error
}

// SessionState is the narrow surface for cursor persistence.
type SessionState interface {
	UpsertLastMsgID(ctx context.Context, channelID, lastMsgID int64) error
	GetLastMsgID(ctx context.Context, channelID int64) (int64, error)
}

// Config holds tunables for Worker.
type Config struct {
	PollInterval time.Duration
	HTTPTimeout  time.Duration
	UserAgent    string
}

// Worker polls a set of channels on a fixed cadence and publishes any
// previously-unseen posts onto queue.SubjectRaw.
type Worker struct {
	channels []ChannelSpec
	cfg      Config
	http     *http.Client
	pub      Publisher
	state    SessionState
	// internalID maps username -> Postgres channels.id; populated at startup.
	internalID map[string]int64
	mu         sync.Mutex
}

// NewWorker constructs a worker. internalIDs must contain the resolved
// internal channels.id for every spec.Username.
func NewWorker(
	specs []ChannelSpec,
	cfg Config,
	pub Publisher,
	state SessionState,
	internalIDs map[string]int64,
) *Worker {
	if cfg.PollInterval == 0 {
		cfg.PollInterval = 60 * time.Second
	}
	if cfg.HTTPTimeout == 0 {
		cfg.HTTPTimeout = 15 * time.Second
	}
	if cfg.UserAgent == "" {
		cfg.UserAgent = "mute-bot/0.1 (+https://example.com)"
	}
	return &Worker{
		channels:   specs,
		cfg:        cfg,
		http:       &http.Client{Timeout: cfg.HTTPTimeout},
		pub:        pub,
		state:      state,
		internalID: internalIDs,
	}
}

// Run drives the polling loop until ctx is cancelled. One tick polls all
// channels sequentially; if a single channel fails the worker logs and moves
// on so a single dead channel does not stall the whole pipeline.
func (w *Worker) Run(ctx context.Context) error {
	t := time.NewTicker(w.cfg.PollInterval)
	defer t.Stop()
	// Tick once immediately so we don't wait a full interval after start.
	w.tick(ctx)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			w.tick(ctx)
		}
	}
}

func (w *Worker) tick(ctx context.Context) {
	for _, ch := range w.channels {
		if err := w.pollChannel(ctx, ch); err != nil {
			slog.Warn("tgscraper: channel poll failed",
				slog.String("channel", ch.Username),
				slog.Any("err", err),
			)
		}
	}
}

func (w *Worker) pollChannel(ctx context.Context, ch ChannelSpec) error {
	w.mu.Lock()
	internalID, ok := w.internalID[ch.Username]
	w.mu.Unlock()
	if !ok {
		return fmt.Errorf("unknown internal id for channel %q", ch.Username)
	}

	lastSeen, err := w.state.GetLastMsgID(ctx, internalID)
	if err != nil {
		return fmt.Errorf("get cursor: %w", err)
	}

	body, err := w.fetch(ctx, ch.Username)
	if err != nil {
		return fmt.Errorf("fetch: %w", err)
	}
	posts, err := ParseChannelHTML(body, ch.Username)
	if err != nil {
		return fmt.Errorf("parse: %w", err)
	}

	maxSeen := lastSeen
	published := 0
	for _, p := range posts {
		if p.TGMsgID <= lastSeen {
			continue
		}
		if err := w.pub.Publish(ctx, queue.SubjectRaw, p); err != nil {
			slog.Error("tgscraper: publish failed",
				slog.String("channel", ch.Username),
				slog.Int64("msg_id", p.TGMsgID),
				slog.Any("err", err),
			)
			continue
		}
		published++
		if p.TGMsgID > maxSeen {
			maxSeen = p.TGMsgID
		}
	}
	if maxSeen > lastSeen {
		if err := w.state.UpsertLastMsgID(ctx, internalID, maxSeen); err != nil {
			return fmt.Errorf("upsert cursor: %w", err)
		}
	}
	if published > 0 {
		slog.Info("tgscraper: published",
			slog.String("channel", ch.Username),
			slog.Int("count", published),
			slog.Int64("last_msg_id", maxSeen),
		)
	}
	return nil
}

func (w *Worker) fetch(ctx context.Context, username string) (string, error) {
	url := fmt.Sprintf("https://t.me/s/%s", username)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", w.cfg.UserAgent)
	req.Header.Set("Accept-Language", "ru,en;q=0.8")
	res, err := w.http.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status %d", res.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, 5*1024*1024))
	if err != nil {
		return "", err
	}
	return string(raw), nil
}
