// Package alerts implements the cluster.scored subscriber that pushes
// real-time breaking-news alerts to Pro users whose alert_threshold has
// been crossed by the just-ranked cluster.
//
// INV-2 (Free users never pay per-event LLM cost) is preserved by the
// ListEligible query — only Pro + alerts_enabled users are enumerated,
// so the per-message work (threshold check, throttle SET NX) never runs
// for Free users.
//
// INV-5 (Free user latency budget) is preserved because the alerts
// pipeline never blocks the digest pipeline: it runs as its own durable
// consumer on the same JetStream subject and shares no critical section
// with the ranker.
package alerts

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"
)

// EligibleUser bundles every field the worker needs to make a per-user
// decision in one row, avoiding N+1 lookups in the hot path.
type EligibleUser struct {
	UserID         int64
	TGUserID       int64
	AlertThreshold int
	ThrottleMin    int
}

// UsersForAlert returns the Pro + alerts_enabled slice the worker iterates
// over per cluster.scored event. Production implementation joins users +
// user_settings server-side; tests can substitute a fixed list.
type UsersForAlert interface {
	ListEligible(ctx context.Context) ([]EligibleUser, error)
}

// Throttler decides whether a per-(user, topic) push is allowed right now.
// Returns true when the slot was acquired (push allowed), false when a
// previous alert in the same window is still holding it. Release lets the
// worker undo a freshly-acquired slot when the subsequent send fails so
// the user is not locked out for the whole TTL despite never receiving
// the alert.
type Throttler interface {
	Allow(ctx context.Context, userID int64, topic string, ttl time.Duration) (bool, error)
	Release(ctx context.Context, userID int64, topic string) error
}

// Cluster is the read model the worker needs from the clusters store. The
// type is local so the worker stays free of the postgres package.
type Cluster struct {
	Headline string
	Summary  string
	Topics   []string
	Coverage int
	Score    float32
}

// ClusterReader fetches the cluster the worker is about to alert on. The
// classifier publishes a {"cluster_id": N} payload; everything else
// (headline, summary, score, topics) comes from the persisted row.
type ClusterReader interface {
	Get(ctx context.Context, id int64) (Cluster, error)
}

// TopicMatcher is the optional custom-topic filter. When Pro users have
// defined their own topics via /topics add, the worker forwards an alert
// only when the cluster's centroid matches at least one of them. When
// MatchesAny returns false the alert is dropped for that user.
//
// Free users never reach this code path (they're filtered out before
// ListEligible enumeration), so MatchesAny is only called for Pro users.
// Implementations should return (true, nil) when the user has no custom
// topics — that is, the user accepts every preset-classified cluster.
type TopicMatcher interface {
	MatchesAny(ctx context.Context, userID, clusterID int64) (bool, error)
}

// PassThroughTopics is a zero-config TopicMatcher that always returns
// true. Used in production until the topics package wires its real
// matcher; keeps the alerts worker independent of M4's roll-out.
type PassThroughTopics struct{}

// MatchesAny implements TopicMatcher by always accepting.
func (PassThroughTopics) MatchesAny(_ context.Context, _, _ int64) (bool, error) {
	return true, nil
}

// Sender pushes a single text message to the user's Telegram chat. The
// rate-limited bot.Sender (per-chat token bucket) satisfies this contract
// via its SendDigest method.
type Sender interface {
	SendDigest(ctx context.Context, tgUserID int64, text string) error
}

// Event is the JetStream payload published on cluster.scored. Only
// cluster_id is wire-stable; the rest of the alert is derived from the
// persisted cluster row.
type Event struct {
	ClusterID int64 `json:"cluster_id"`
}

// Deps configures the Worker.
type Deps struct {
	Users    UsersForAlert
	Clusters ClusterReader
	Topics   TopicMatcher
	Throttle Throttler
	Sender   Sender
	Logger   *slog.Logger
}

// Worker handles cluster.scored events. One Handler is shared by every
// durable delivery — the durable consumer name in cmd/processor wires it
// to its own NATS consumer so it does not race with the ranker.
type Worker struct{ d Deps }

// NewWorker constructs a Worker. Users, Clusters, Throttle and Sender are
// required; Topics defaults to PassThroughTopics{} and Logger to
// slog.Default() when nil. Defaults preserve the worker's invariants:
// Topics=pass-through means M4's absence does not silently drop alerts.
func NewWorker(d Deps) *Worker {
	if d.Topics == nil {
		d.Topics = PassThroughTopics{}
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	return &Worker{d: d}
}

// Handle is the JetStream callback for cluster.scored. It enumerates
// every eligible Pro user, runs the per-user threshold + custom-topic +
// throttle decisions, and pushes the alert through Sender. Errors from
// any single user are logged and swallowed so a transient Sender failure
// for user A does not stall delivery to user B.
func (w *Worker) Handle(ctx context.Context, payload []byte) error {
	var ev Event
	if err := json.Unmarshal(payload, &ev); err != nil {
		return fmt.Errorf("unmarshal cluster.scored: %w", err)
	}
	if ev.ClusterID == 0 {
		return fmt.Errorf("cluster.scored: zero cluster_id")
	}

	cl, err := w.d.Clusters.Get(ctx, ev.ClusterID)
	if err != nil {
		return fmt.Errorf("get cluster: %w", err)
	}

	users, err := w.d.Users.ListEligible(ctx)
	if err != nil {
		return fmt.Errorf("list eligible: %w", err)
	}

	scorePct := int(cl.Score * 100)
	topicKey := firstNonEmpty(cl.Topics)

	for _, u := range users {
		if scorePct < u.AlertThreshold {
			continue
		}
		// Custom topics gate (Pro only): when MatchesAny returns false
		// the user has narrower interests than the cluster's preset
		// classification, so skip. Errors fall open (accept) so that a
		// transient embedding-store outage does not silence alerts.
		matches, mErr := w.d.Topics.MatchesAny(ctx, u.UserID, ev.ClusterID)
		if mErr != nil {
			w.d.Logger.WarnContext(ctx, "alerts: topic match",
				slog.Int64("user", u.UserID), slog.Int64("cluster", ev.ClusterID),
				slog.Any("err", mErr))
		} else if !matches {
			continue
		}

		// Guard against malformed settings that would push the throttle
		// TTL negative (Redis SETEX rejects non-positive seconds). Falls
		// back to the same 30-minute default as the migration.
		throttleMin := u.ThrottleMin
		if throttleMin <= 0 {
			throttleMin = 30
		}
		ok, err := w.d.Throttle.Allow(ctx, u.UserID, topicKey,
			time.Duration(throttleMin)*time.Minute)
		if err != nil {
			w.d.Logger.WarnContext(ctx, "alerts: throttle",
				slog.Int64("user", u.UserID), slog.Any("err", err))
			continue
		}
		if !ok {
			continue
		}

		text := formatAlert(cl, u.AlertThreshold, scorePct)
		if err := w.d.Sender.SendDigest(ctx, u.TGUserID, text); err != nil {
			w.d.Logger.WarnContext(ctx, "alerts: send",
				slog.Int64("user", u.UserID), slog.Any("err", err))
			// Send failed: free the throttle slot so the next delivery
			// attempt can try again. Without this the user is locked
			// out for the entire TTL despite never receiving the alert.
			if rErr := w.d.Throttle.Release(ctx, u.UserID, topicKey); rErr != nil {
				w.d.Logger.WarnContext(ctx, "alerts: throttle release",
					slog.Int64("user", u.UserID), slog.Any("err", rErr))
			}
		}
	}
	return nil
}

// formatAlert renders the message body shown to the user. Format matches
// the Phase 2 spec verbatim so dogfooders see the same shape the plan
// promises.
func formatAlert(c Cluster, threshold, scorePct int) string {
	return fmt.Sprintf("⚠️ Важное: %s\nИсточники: %d\nПорог сработал: %d/%d",
		c.Headline, c.Coverage, scorePct, threshold)
}

// firstNonEmpty returns the first non-empty string in s; "other" when
// every entry is empty or the slice is nil. Used as the throttle topic
// key so users do not get re-paged on every news cluster within their
// throttle window when the cluster carries no topic label.
func firstNonEmpty(s []string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return "other"
}
