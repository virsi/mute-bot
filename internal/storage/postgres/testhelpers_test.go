//go:build integration

package postgres

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// setupTestPool returns a pooled connection to the integration Postgres
// pointed at by POSTGRES_DSN. The test is skipped when the env var is unset
// so unit-test runs without infra remain green. The pool is closed via
// t.Cleanup so callers do not need to defer.
func setupTestPool(t *testing.T) *Pool {
	t.Helper()
	dsn := os.Getenv("POSTGRES_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_DSN unset")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	p, err := NewPool(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(p.Close)
	return p
}

// truncate wipes the named tables and resets their identity sequences. The
// CASCADE keeps the call short for callers — they pass the leaf table they
// care about and the rest follows.
func truncate(t *testing.T, p *Pool, tables string) {
	t.Helper()
	q := fmt.Sprintf("TRUNCATE %s RESTART IDENTITY CASCADE", tables)
	_, err := p.Pool().Exec(context.Background(), q)
	require.NoError(t, err)
}
