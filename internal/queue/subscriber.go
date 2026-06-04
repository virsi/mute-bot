package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// Handler is the per-message callback invoked by Subscriber.Run. Returning
// nil ACKs the message; returning a non-nil error triggers a delayed NACK
// (retry) or, once MaxDeliver is reached, parks the payload in the DLQ.
type Handler func(ctx context.Context, data []byte) error

// BackoffFunc returns the delay before re-delivery after `attempt` failed
// deliveries. Attempt counting starts at 1.
type BackoffFunc func(attempt int) time.Duration

// SubscribeConfig configures a durable JetStream pull-consumer plus the
// retry/DLQ behaviour applied to its handler.
type SubscribeConfig struct {
	Stream     string
	Subject    string
	Durable    string        // durable consumer name (must be unique per stream)
	MaxDeliver int           // total delivery attempts before DLQ (default 5)
	AckWait    time.Duration // server-side ack timeout (default 30s)
	Backoff    BackoffFunc   // override for retry delay (default defaultBackoff)
	Handler    Handler
}

// DLQEnvelope is the payload published to `<subject>.dlq` once a message has
// exhausted MaxDeliver. It preserves the original bytes plus diagnostic
// metadata so a human or replay job can inspect or re-publish it.
type DLQEnvelope struct {
	OriginalSubject string    `json:"original_subject"`
	Payload         []byte    `json:"payload"`
	Error           string    `json:"error"`
	DeliveryCount   uint64    `json:"delivery_count"`
	FailedAt        time.Time `json:"failed_at"`
}

// Subscriber drives a durable pull consumer over a single subject and applies
// retry-with-backoff + DLQ semantics around a user-supplied Handler.
type Subscriber struct {
	c *Conn
}

// NewSubscriber constructs a Subscriber bound to the given connection.
func NewSubscriber(c *Conn) *Subscriber { return &Subscriber{c: c} }

// Run consumes messages on cfg.Subject from cfg.Stream until ctx is cancelled.
// It blocks; spawn a goroutine if you need fire-and-forget. The function
// returns ctx.Err() on cancellation, or a setup error if the consumer cannot
// be created.
func (s *Subscriber) Run(ctx context.Context, cfg SubscribeConfig) error {
	if cfg.Handler == nil {
		return errors.New("subscriber: Handler is required")
	}
	if cfg.Stream == "" || cfg.Subject == "" || cfg.Durable == "" {
		return errors.New("subscriber: Stream, Subject and Durable are required")
	}
	if cfg.MaxDeliver <= 0 {
		cfg.MaxDeliver = 5
	}
	if cfg.AckWait <= 0 {
		cfg.AckWait = 30 * time.Second
	}
	if cfg.Backoff == nil {
		cfg.Backoff = defaultBackoff
	}

	cons, err := s.c.JS().CreateOrUpdateConsumer(ctx, cfg.Stream, jetstream.ConsumerConfig{
		Durable:       cfg.Durable,
		FilterSubject: cfg.Subject,
		AckPolicy:     jetstream.AckExplicitPolicy,
		MaxDeliver:    cfg.MaxDeliver,
		AckWait:       cfg.AckWait,
		MaxAckPending: 256,
	})
	if err != nil {
		return fmt.Errorf("create consumer %s/%s: %w", cfg.Stream, cfg.Durable, err)
	}

	iter, err := cons.Messages()
	if err != nil {
		return fmt.Errorf("open messages iterator: %w", err)
	}
	defer iter.Stop()

	// Stop the iterator promptly when the caller cancels ctx — otherwise
	// iter.Next blocks until the next message regardless of ctx.
	go func() {
		<-ctx.Done()
		iter.Stop()
	}()

	pub := NewPublisher(s.c)

	for {
		msg, err := iter.Next()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// Transient iterator error: log and retry after a short pause.
			slog.Warn("queue: iter.Next failed", "stream", cfg.Stream, "subject", cfg.Subject, "err", err)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Second):
			}
			continue
		}
		s.handleMessage(ctx, cfg, pub, msg)
	}
}

// handleMessage runs the user handler, then ACKs, NACKs-with-delay, or sends
// the payload to the DLQ depending on the outcome and current delivery count.
func (s *Subscriber) handleMessage(ctx context.Context, cfg SubscribeConfig, pub *Publisher, msg jetstream.Msg) {
	md, mdErr := msg.Metadata()
	delivered := uint64(1)
	if mdErr == nil && md != nil {
		delivered = md.NumDelivered
	}

	herr := cfg.Handler(ctx, msg.Data())
	if herr == nil {
		if err := msg.Ack(); err != nil {
			slog.Warn("queue: ack failed", "subject", cfg.Subject, "err", err)
		}
		return
	}

	// #nosec G115 -- cfg.MaxDeliver is validated >0 at Run() entry
	if delivered >= uint64(cfg.MaxDeliver) {
		s.sendToDLQ(ctx, cfg, pub, msg, herr, delivered)
		return
	}

	delay := cfg.Backoff(int(delivered))
	if err := msg.NakWithDelay(delay); err != nil {
		slog.Warn("queue: nak failed", "subject", cfg.Subject, "err", err)
	}
}

// sendToDLQ publishes the DLQ envelope and ACKs the original message off the
// main subject. ACK is best-effort: if the DLQ publish itself fails we leave
// the message un-ACKed so the broker eventually drops or replays it instead
// of silently losing it.
func (s *Subscriber) sendToDLQ(ctx context.Context, cfg SubscribeConfig, pub *Publisher, msg jetstream.Msg, herr error, delivered uint64) {
	env := DLQEnvelope{
		OriginalSubject: cfg.Subject,
		Payload:         msg.Data(),
		Error:           herr.Error(),
		DeliveryCount:   delivered,
		FailedAt:        time.Now().UTC(),
	}
	data, err := json.Marshal(env)
	if err != nil {
		slog.Error("queue: marshal DLQ envelope failed", "subject", cfg.Subject, "err", err)
		_ = msg.Nak()
		return
	}
	dlqSubject := cfg.Subject + DLQSuffix
	if err := pub.PublishRaw(ctx, dlqSubject, data); err != nil {
		slog.Error("queue: DLQ publish failed", "subject", dlqSubject, "err", err)
		_ = msg.Nak()
		return
	}
	slog.Error("queue: message sent to DLQ",
		"subject", cfg.Subject,
		"dlq", dlqSubject,
		"delivered", delivered,
		"handler_err", herr,
	)
	if err := msg.Ack(); err != nil {
		slog.Warn("queue: ack after DLQ failed", "subject", cfg.Subject, "err", err)
	}
}

// defaultBackoff implements the schedule documented in the spec:
// 2s, 5s, 15s, then 60s for every subsequent attempt.
func defaultBackoff(attempt int) time.Duration {
	switch {
	case attempt <= 1:
		return 2 * time.Second
	case attempt == 2:
		return 5 * time.Second
	case attempt == 3:
		return 15 * time.Second
	default:
		return 60 * time.Second
	}
}
