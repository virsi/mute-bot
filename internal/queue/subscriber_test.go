//go:build integration

package queue

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"
)

// TestSubscriber_HandlesAndRetries verifies that a handler failure causes
// re-delivery and a subsequent success ACKs the message.
func TestSubscriber_HandlesAndRetries(t *testing.T) {
	url := os.Getenv("NATS_URL")
	if url == "" {
		t.Skip("NATS_URL unset")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	c, err := Connect(ctx, url)
	require.NoError(t, err)
	defer c.Close()
	require.NoError(t, c.EnsureStreams(ctx))
	purgeSubject(t, ctx, c, StreamIngest, SubjectRaw)

	durable := uniqueDurable(t)
	defer func() { _ = c.JS().DeleteConsumer(ctx, StreamIngest, durable) }()

	pub := NewPublisher(c)
	require.NoError(t, pub.Publish(ctx, SubjectRaw, map[string]string{"k": "v"}))

	var attempts int32
	sub := NewSubscriber(c)

	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()

	go func() {
		_ = sub.Run(runCtx, SubscribeConfig{
			Stream:     StreamIngest,
			Subject:    SubjectRaw,
			Durable:    durable,
			MaxDeliver: 3,
			AckWait:    2 * time.Second,
			Backoff:    func(_ int) time.Duration { return 100 * time.Millisecond },
			Handler: func(_ context.Context, data []byte) error {
				n := atomic.AddInt32(&attempts, 1)
				if n < 2 {
					return errors.New("retry me")
				}
				return nil
			},
		})
	}()

	require.Eventually(t,
		func() bool { return atomic.LoadInt32(&attempts) >= 2 },
		10*time.Second, 50*time.Millisecond,
		"expected handler to be invoked at least twice (retry path)",
	)
}

// TestSubscriber_DLQOnExhaustion verifies that after MaxDeliver failures the
// message is parked on the DLQ subject and ACKed off the main subject.
func TestSubscriber_DLQOnExhaustion(t *testing.T) {
	url := os.Getenv("NATS_URL")
	if url == "" {
		t.Skip("NATS_URL unset")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	c, err := Connect(ctx, url)
	require.NoError(t, err)
	defer c.Close()
	require.NoError(t, c.EnsureStreams(ctx))
	purgeSubject(t, ctx, c, StreamIngest, SubjectRaw)
	dlqSubject := SubjectRaw + DLQSuffix
	purgeSubject(t, ctx, c, StreamIngest, dlqSubject)

	durable := uniqueDurable(t)
	defer func() { _ = c.JS().DeleteConsumer(ctx, StreamIngest, durable) }()

	pub := NewPublisher(c)
	require.NoError(t, pub.Publish(ctx, SubjectRaw, map[string]string{"bad": "payload"}))

	var attempts int32
	sub := NewSubscriber(c)

	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()

	const maxDeliver = 2
	go func() {
		_ = sub.Run(runCtx, SubscribeConfig{
			Stream:     StreamIngest,
			Subject:    SubjectRaw,
			Durable:    durable,
			MaxDeliver: maxDeliver,
			AckWait:    1 * time.Second,
			Backoff:    func(_ int) time.Duration { return 50 * time.Millisecond },
			Handler: func(_ context.Context, _ []byte) error {
				atomic.AddInt32(&attempts, 1)
				return errors.New("always fail")
			},
		})
	}()

	// DLQ consumer to verify the parked envelope.
	dlqCons, err := c.JS().CreateOrUpdateConsumer(ctx, StreamIngest, jetstream.ConsumerConfig{
		Name:          "dlq-watch-" + t.Name(),
		FilterSubject: dlqSubject,
		AckPolicy:     jetstream.AckExplicitPolicy,
	})
	require.NoError(t, err)
	defer func() { _ = c.JS().DeleteConsumer(ctx, StreamIngest, dlqCons.CachedInfo().Name) }()

	msg, err := dlqCons.Next(jetstream.FetchMaxWait(15 * time.Second))
	require.NoError(t, err, "expected DLQ message after %d attempts", maxDeliver)

	var env DLQEnvelope
	require.NoError(t, json.Unmarshal(msg.Data(), &env))
	require.Equal(t, SubjectRaw, env.OriginalSubject)
	require.Contains(t, env.Error, "always fail")
	require.NotEmpty(t, env.Payload)
	require.NoError(t, msg.Ack())

	require.GreaterOrEqual(t, int(atomic.LoadInt32(&attempts)), maxDeliver,
		"handler must have been invoked MaxDeliver times before DLQ")
}

func uniqueDurable(t *testing.T) string {
	t.Helper()
	// Durable names must not contain '/', '.', '*' or '>'.
	name := "test-" + t.Name() + "-" + time.Now().Format("150405.000")
	out := make([]byte, 0, len(name))
	for _, r := range name {
		switch r {
		case '/', '.', '*', '>', ' ':
			out = append(out, '-')
		default:
			out = append(out, byte(r))
		}
	}
	return string(out)
}
