//go:build integration

package postgres

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPool_PingAndClose(t *testing.T) {
	dsn := os.Getenv("POSTGRES_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_DSN unset")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	p, err := NewPool(ctx, dsn)
	require.NoError(t, err)
	defer p.Close()

	var one int
	require.NoError(t, p.Pool().QueryRow(ctx, "SELECT 1").Scan(&one))
	require.Equal(t, 1, one)
}
