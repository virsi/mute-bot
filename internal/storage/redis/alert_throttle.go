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
	key := fmt.Sprintf("alert_throttle:%d:%s", userID, topic)
	ok, err := t.rdb.SetNX(ctx, key, "1", ttl).Result()
	if err != nil {
		return false, fmt.Errorf("setnx %s: %w", key, err)
	}
	return ok, nil
}
