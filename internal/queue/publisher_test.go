//go:build integration

package queue

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"
)

func TestPublisher_PublishJSON(t *testing.T) {
	url := os.Getenv("NATS_URL")
	if url == "" {
		t.Skip("NATS_URL unset")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	c, err := Connect(ctx, url)
	require.NoError(t, err)
	defer c.Close()
	require.NoError(t, c.EnsureStreams(ctx))

	// Drain leftover messages from previous test runs on this subject so the
	// assertion below is not polluted.
	purgeSubject(t, ctx, c, StreamIngest, SubjectRaw)

	pub := NewPublisher(c)
	type evt struct {
		Hello string `json:"hello"`
	}
	require.NoError(t, pub.Publish(ctx, SubjectRaw, evt{Hello: "world"}))

	// WorkQueuePolicy streams disallow ordered consumers; use an explicit
	// ephemeral pull consumer with explicit ack instead.
	cons, err := c.JS().CreateOrUpdateConsumer(ctx, StreamIngest, jetstream.ConsumerConfig{
		Name:          "pub-test-" + t.Name(),
		FilterSubject: SubjectRaw,
		AckPolicy:     jetstream.AckExplicitPolicy,
	})
	require.NoError(t, err)
	defer func() { _ = c.JS().DeleteConsumer(ctx, StreamIngest, cons.CachedInfo().Name) }()

	msg, err := cons.Next(jetstream.FetchMaxWait(3 * time.Second))
	require.NoError(t, err)

	var got evt
	require.NoError(t, json.Unmarshal(msg.Data(), &got))
	require.Equal(t, "world", got.Hello)
	require.NoError(t, msg.Ack())
}

func TestPublisher_MarshalError(t *testing.T) {
	url := os.Getenv("NATS_URL")
	if url == "" {
		t.Skip("NATS_URL unset")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, err := Connect(ctx, url)
	require.NoError(t, err)
	defer c.Close()
	require.NoError(t, c.EnsureStreams(ctx))

	pub := NewPublisher(c)
	// channels cannot be JSON-marshalled.
	err = pub.Publish(ctx, SubjectRaw, make(chan int))
	require.Error(t, err)
}

// purgeSubject removes all retained messages on the given subject so a
// fresh-start test sees only its own publish.
func purgeSubject(t *testing.T, ctx context.Context, c *Conn, stream, subject string) {
	t.Helper()
	s, err := c.JS().Stream(ctx, stream)
	require.NoError(t, err)
	require.NoError(t, s.Purge(ctx, jetstream.WithPurgeSubject(subject)))
}
