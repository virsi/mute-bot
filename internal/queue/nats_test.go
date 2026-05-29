//go:build integration

package queue

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestConnect_AndEnsureStreams(t *testing.T) {
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

	// Re-running EnsureStreams must be idempotent.
	require.NoError(t, c.EnsureStreams(ctx))

	js := c.JS()
	for _, name := range []string{StreamIngest, StreamClusters, StreamDelivery} {
		s, err := js.Stream(ctx, name)
		require.NoError(t, err)
		require.Equal(t, name, s.CachedInfo().Config.Name)
	}
}
