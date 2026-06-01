// Package scheduler triggers digest assembly and other periodic jobs.
//
// It uses Postgres advisory locks to coordinate work across multiple
// scheduler replicas: only one replica acquires a given key at a time
// and runs the job, while the others observe the lock failure and skip.
package scheduler

import (
	"context"
	"fmt"

	"github.com/virsi/mute-bot/internal/storage/postgres"
)

// TryLock attempts to acquire a session-scoped advisory lock identified by
// key. It returns true if the lock was acquired, false if another session
// already holds it. The lock is released either by Unlock or when the
// underlying connection's session terminates.
//
// Advisory locks are re-entrant per session: a session that already holds
// the lock can call TryLock with the same key and get true again. Callers
// that need to detect contention from within a single process must drive
// the lock from independent pool connections (e.g. distinct *Pool instances).
func TryLock(ctx context.Context, p *postgres.Pool, key int64) (bool, error) {
	var ok bool
	if err := p.Pool().QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, key).Scan(&ok); err != nil {
		return false, fmt.Errorf("advisory lock: %w", err)
	}
	return ok, nil
}

// Unlock releases a session-scoped advisory lock previously acquired by
// TryLock with the same key. It is a no-op if the session does not hold
// the lock (pg_advisory_unlock returns false in that case but does not
// raise an error).
func Unlock(ctx context.Context, p *postgres.Pool, key int64) error {
	if _, err := p.Pool().Exec(ctx, `SELECT pg_advisory_unlock($1)`, key); err != nil {
		return fmt.Errorf("advisory unlock: %w", err)
	}
	return nil
}
