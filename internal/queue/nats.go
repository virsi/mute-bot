package queue

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// streamMaxAge controls how long unacked messages live in JetStream before
// the broker drops them. 72h matches the operational SLA — anything older
// is considered stale and is best re-ingested rather than replayed.
const streamMaxAge = 72 * time.Hour

// Conn is a thin wrapper around a NATS connection plus its JetStream context.
// It is safe to share across goroutines; Close must be called exactly once.
type Conn struct {
	nc *nats.Conn
	js jetstream.JetStream
}

// Connect dials NATS and constructs a JetStream context. It keeps reconnect
// retries unbounded so transient broker restarts do not crash workers.
func Connect(ctx context.Context, url string) (*Conn, error) {
	nc, err := nats.Connect(url,
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2*time.Second),
	)
	if err != nil {
		return nil, fmt.Errorf("nats connect: %w", err)
	}
	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("jetstream new: %w", err)
	}
	// Ensure the connection is actually reachable before returning. Without
	// this an unreachable broker only surfaces on first publish/consume.
	// FlushWithContext requires the context to carry a deadline; if the caller
	// passed a Background-like context, supply a sane default so production
	// main()s don't have to wrap every call themselves.
	flushCtx := ctx
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		flushCtx, cancel = context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
	}
	if err := nc.FlushWithContext(flushCtx); err != nil {
		nc.Close()
		return nil, fmt.Errorf("nats flush: %w", err)
	}
	return &Conn{nc: nc, js: js}, nil
}

// Close drains and closes the underlying NATS connection.
func (c *Conn) Close() {
	if c.nc != nil {
		c.nc.Close()
	}
}

// JS returns the JetStream context for advanced operations (consumer creation,
// stream inspection, etc.).
func (c *Conn) JS() jetstream.JetStream { return c.js }

// NC returns the raw NATS connection — useful for tests and core (non-JS) ops.
func (c *Conn) NC() *nats.Conn { return c.nc }

// EnsureStreams creates the INGEST, CLUSTERS and DELIVERY streams if missing,
// or updates them in place if their configuration drifted. The function is
// idempotent — safe to call on every worker start.
func (c *Conn) EnsureStreams(ctx context.Context) error {
	configs := []jetstream.StreamConfig{
		{
			Name:      StreamIngest,
			Subjects:  []string{"ingest.>"},
			Retention: jetstream.WorkQueuePolicy,
			MaxAge:    streamMaxAge,
		},
		{
			Name:      StreamClusters,
			Subjects:  []string{"cluster.>"},
			Retention: jetstream.WorkQueuePolicy,
			MaxAge:    streamMaxAge,
		},
		{
			Name:      StreamDelivery,
			Subjects:  []string{"delivery.>"},
			Retention: jetstream.WorkQueuePolicy,
			MaxAge:    streamMaxAge,
		},
	}
	for _, cfg := range configs {
		if err := ensureStream(ctx, c.js, cfg); err != nil {
			return err
		}
	}
	return nil
}

func ensureStream(ctx context.Context, js jetstream.JetStream, cfg jetstream.StreamConfig) error {
	_, err := js.CreateStream(ctx, cfg)
	if err == nil {
		return nil
	}
	if !errors.Is(err, jetstream.ErrStreamNameAlreadyInUse) {
		// Some servers return the error wrapped; fall back to update either way.
		if _, uerr := js.UpdateStream(ctx, cfg); uerr != nil {
			return fmt.Errorf("ensure stream %s: create=%w update=%v", cfg.Name, err, uerr)
		}
		return nil
	}
	if _, err := js.UpdateStream(ctx, cfg); err != nil {
		return fmt.Errorf("update stream %s: %w", cfg.Name, err)
	}
	return nil
}
