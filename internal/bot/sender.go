// Package bot wraps the Telegram Bot API for the digest delivery path.
//
// Two pieces live here:
//   - Sender: a transport-agnostic rate limiter that throttles outgoing
//     messages per chat (token bucket). Implements digest.Sender.
//   - BotAPI: the concrete go-telegram/bot adapter that Sender calls
//     through the API interface (in api.go).
package bot

import (
	"context"
	"sync"
	"time"
)

// API is the minimal contract Sender needs from the underlying Bot API
// client. Lets us unit-test the rate limiter without a real bot.
type API interface {
	Send(ctx context.Context, chatID int64, text string) error
}

// SenderDeps configures a Sender.
type SenderDeps struct {
	// API is the underlying transport. Required.
	API API
	// PerChatPerSec is the per-chat token-bucket refill rate. Telegram's
	// per-chat limit is ~1 msg/sec; defaults to 1 when unset.
	PerChatPerSec int
}

// Sender enforces Telegram's per-chat rate limit using a token bucket per
// chat id. The bucket starts full (PerChatPerSec tokens), refills at the
// same rate, and SendDigest blocks (respecting ctx) until a token is
// available.
//
// This is intentionally minimal for Phase 1 — no global rate limit, no
// retry-on-429. Both can be added later without changing the digest.Sender
// contract.
type Sender struct {
	d       SenderDeps
	mu      sync.Mutex
	buckets map[int64]*bucket
}

type bucket struct {
	tokens     float64
	lastRefill time.Time
}

// NewSender constructs a Sender with sensible defaults.
func NewSender(d SenderDeps) *Sender {
	if d.PerChatPerSec <= 0 {
		d.PerChatPerSec = 1
	}
	return &Sender{d: d, buckets: make(map[int64]*bucket)}
}

// SendDigest implements digest.Sender: it waits for a per-chat rate-limit
// slot, then forwards to the underlying API. Returns ctx.Err() if the
// context expires before a slot is available.
func (s *Sender) SendDigest(ctx context.Context, chatID int64, text string) error {
	if err := s.waitForToken(ctx, chatID); err != nil {
		return err
	}
	return s.d.API.Send(ctx, chatID, text)
}

// waitForToken blocks until a token is available in chatID's bucket or ctx
// expires. The bucket is refilled lazily on each call rather than via a
// background goroutine — there is no per-chat goroutine to leak.
func (s *Sender) waitForToken(ctx context.Context, chatID int64) error {
	rate := float64(s.d.PerChatPerSec)
	for {
		s.mu.Lock()
		b, ok := s.buckets[chatID]
		now := time.Now()
		if !ok {
			b = &bucket{tokens: rate, lastRefill: now}
			s.buckets[chatID] = b
		}
		elapsed := now.Sub(b.lastRefill).Seconds()
		b.tokens += elapsed * rate
		if b.tokens > rate {
			b.tokens = rate
		}
		b.lastRefill = now

		if b.tokens >= 1 {
			b.tokens--
			s.mu.Unlock()
			return nil
		}
		// Compute wait outside the lock — we don't want to hold the mutex
		// across a select that may block for ~1s.
		need := (1 - b.tokens) / rate
		s.mu.Unlock()

		timer := time.NewTimer(time.Duration(need * float64(time.Second)))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
