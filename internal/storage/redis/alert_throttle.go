package redis

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// AlertThrottle gates real-time alert pushes per (user, topic). It uses
// SET key value NX EX ttl so the first caller within a throttle window
// acquires the slot and every subsequent caller within the same window is
// denied — atomic, no read-modify-write, and self-expiring so nothing
// needs to be cleaned up by the application layer.
type AlertThrottle struct{ rdb *redis.Client }

// NewAlertThrottle constructs an AlertThrottle bound to c.
func NewAlertThrottle(c *Client) *AlertThrottle { return &AlertThrottle{rdb: c.RDB()} }

// throttleKey builds the Redis key used by Allow and Release. Kept private
// so callers can't accidentally release a key they did not acquire.
func throttleKey(userID int64, topic string) string {
	return fmt.Sprintf("alert_throttle:%d:%s", userID, topic)
}

// Allow attempts to acquire the throttle slot for (userID, topic) with the
// given TTL. Returns true when the slot was acquired (the caller may push
// the alert) and false when a previous alert in the same window is still
// holding the slot. A zero or negative ttl is treated as "no throttle" —
// the call still tries SETNX with a 1s TTL so that bursty traffic does not
// emit duplicates within the same JetStream delivery batch.
func (t *AlertThrottle) Allow(ctx context.Context, userID int64, topic string, ttl time.Duration) (bool, error) {
	if ttl <= 0 {
		ttl = time.Second
	}
	key := throttleKey(userID, topic)
	ok, err := t.rdb.SetNX(ctx, key, "1", ttl).Result()
	if err != nil {
		return false, fmt.Errorf("setnx %s: %w", key, err)
	}
	return ok, nil
}

// Release deletes the throttle slot for (userID, topic). Used by the
// alerts worker when Allow succeeded but the subsequent SendDigest call
// failed; without it the user would be locked out for the entire TTL
// despite never receiving the alert, and TG-side retry would also be
// silently swallowed.
//
// Best-effort: a Redis blip during Release leaves the key in place,
// which is the same end state as if Release had not been attempted —
// the alert was lost but the next throttle window will recover.
func (t *AlertThrottle) Release(ctx context.Context, userID int64, topic string) error {
	if _, err := t.rdb.Del(ctx, throttleKey(userID, topic)).Result(); err != nil {
		return fmt.Errorf("del throttle: %w", err)
	}
	return nil
}
