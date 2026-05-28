package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	pgxvec "github.com/pgvector/pgvector-go/pgx"
)

// Pool wraps pgxpool.Pool with pgvector type registration applied on every
// new connection. Use Pool() to get the underlying pool when you need to
// run queries directly from repositories.
type Pool struct {
	pool *pgxpool.Pool
}

// NewPool constructs a Pool from a Postgres DSN. The pool registers pgvector
// types (vector, halfvec, sparsevec) on every fresh connection and verifies
// reachability with Ping before returning.
func NewPool(ctx context.Context, dsn string) (*Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse dsn: %w", err)
	}
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		return pgxvec.RegisterTypes(ctx, conn)
	}
	p, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("new pool: %w", err)
	}
	if err := p.Ping(ctx); err != nil {
		p.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	return &Pool{pool: p}, nil
}

// Pool returns the underlying pgxpool.Pool.
func (p *Pool) Pool() *pgxpool.Pool { return p.pool }

// Close releases all pool resources.
func (p *Pool) Close() { p.pool.Close() }
