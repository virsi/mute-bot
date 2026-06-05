package digest

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/pgvector/pgvector-go"

	"github.com/virsi/mute-bot/internal/storage/postgres"
)

// SettingsReader returns the per-user delivery settings.
type SettingsReader interface {
	Get(ctx context.Context, userID int64) (postgres.Settings, error)
}

// UserLoader fetches a user row by internal id. Used by the optional
// custom-topic filter to find out the recipient's tier before paying for
// the per-cluster centroid lookup.
type UserLoader interface {
	GetByID(ctx context.Context, id int64) (postgres.User, error)
}

// TierChecker classifies a loaded user as Pro/Free. Satisfied by
// users.Service in production.
type TierChecker interface {
	IsPro(u postgres.User) bool
}

// TopicMatcher answers whether a cluster centroid matches at least one
// of userID's custom topics. By convention, returns true for users with
// no custom topics so Free users (gated from adding topics) keep the
// default "see everything" behavior.
type TopicMatcher interface {
	MatchesAny(ctx context.Context, userID int64, centroid pgvector.Vector) (bool, error)
}

// Centroider returns the cosine centroid for a cluster's posts. Surfaces
// postgres.ErrNoEmbeddings when the cluster has no embeddings yet so the
// assembler can skip the filter for that cluster without dropping it.
type Centroider interface {
	ClusterCentroid(ctx context.Context, clusterID int64) (pgvector.Vector, error)
}

// ClustersSearcher returns active clusters that match a filter, scored
// descending. Used to pull digest candidates for a user.
type ClustersSearcher interface {
	Search(ctx context.Context, f postgres.ClusterFilter) ([]postgres.Cluster, error)
}

// DeliveriesRW is the read/write contract for the deliveries table. The
// assembler reads ListClusterIDs to exclude already-delivered clusters and
// writes Record after a successful send.
type DeliveriesRW interface {
	ListClusterIDs(ctx context.Context, userID int64, limit int) ([]int64, error)
	Record(ctx context.Context, userID, clusterID int64, channel string) error
}

// SourcesReader returns the channel usernames that contributed posts to a
// cluster. Rendered in the digest under the "📡" line.
type SourcesReader interface {
	SourcesForCluster(ctx context.Context, clusterID int64) ([]string, error)
}

// Sender ships the rendered digest text to the user via the bot.
type Sender interface {
	SendDigest(ctx context.Context, tgUserID int64, text string) error
}

// AssemblerDeps wires the assembler to its collaborators. All fields
// except the Custom* group and Now/HistoryWindow/MaxItems/Logger are
// required. The Custom* group is the M4 Pro-only custom-topic filter:
// every field is nil-safe and a missing value disables the filter
// entirely — useful for tests and for Phase 1 wiring that has not yet
// adopted the Pro path.
type AssemblerDeps struct {
	Settings   SettingsReader
	Clusters   ClustersSearcher
	Deliveries DeliveriesRW
	Sources    SourcesReader
	Sender     Sender

	// CustomTopics, Centroider, Tier and Users together gate the per-
	// cluster custom-topic filter. The filter is skipped when any of
	// these is nil OR the user is not Pro OR the user has no custom
	// topics. See applyCustomTopicFilter for the precedence.
	CustomTopics TopicMatcher
	Centroider   Centroider
	Tier         TierChecker
	Users        UserLoader

	// Now is injectable for tests; defaults to time.Now.
	Now func() time.Time
	// HistoryWindow is the look-back used as ClusterFilter.SinceTime offset.
	// Defaults to 24h.
	HistoryWindow time.Duration
	// MaxItems caps the number of clusters per digest. Defaults to 10.
	MaxItems int
	// Logger receives non-fatal warnings (e.g. failing to record a delivery
	// after a successful send). Defaults to slog.Default().
	Logger *slog.Logger
}

// Assembler orchestrates building and delivering a per-user digest.
type Assembler struct{ d AssemblerDeps }

// NewAssembler constructs an Assembler with sensible defaults filled in.
func NewAssembler(d AssemblerDeps) *Assembler {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.HistoryWindow == 0 {
		d.HistoryWindow = 24 * time.Hour
	}
	if d.MaxItems == 0 {
		d.MaxItems = 10
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	return &Assembler{d: d}
}

// AssembleRequest carries the per-call parameters for Assemble.
type AssembleRequest struct {
	// UserID is the internal users.id (used to look up settings and
	// deliveries).
	UserID int64
	// TGUserID is the Telegram chat id the digest gets sent to.
	TGUserID int64
	// Channel is the delivery channel tag stored in deliveries.channel
	// (e.g. "digest", "alert").
	Channel string
	// Title is the human-facing digest name (shown in the header). Defaults
	// to "Сводка".
	Title string
}

// Assemble pulls candidate clusters according to the user's settings,
// renders them, sends the message, and records the delivered cluster ids so
// they will not appear in future digests for this user.
//
// On an empty result the method is a no-op: no send, no record.
func (a *Assembler) Assemble(ctx context.Context, req AssembleRequest) error {
	if req.Title == "" {
		req.Title = "Сводка"
	}

	s, err := a.d.Settings.Get(ctx, req.UserID)
	if err != nil {
		return fmt.Errorf("settings: %w", err)
	}
	excluded, err := a.d.Deliveries.ListClusterIDs(ctx, req.UserID, 1000)
	if err != nil {
		return fmt.Errorf("excluded: %w", err)
	}

	clusters, err := a.d.Clusters.Search(ctx, postgres.ClusterFilter{
		Topics:     s.Topics,
		MinScore:   float32(s.Threshold) / 100,
		SinceTime:  a.d.Now().Add(-a.d.HistoryWindow),
		ExcludeIDs: excluded,
		Limit:      a.d.MaxItems,
	})
	if err != nil {
		return fmt.Errorf("search: %w", err)
	}
	if len(clusters) == 0 {
		return nil
	}

	clusters, err = a.applyCustomTopicFilter(ctx, req.UserID, clusters)
	if err != nil {
		return fmt.Errorf("custom topic filter: %w", err)
	}
	if len(clusters) == 0 {
		return nil
	}

	items := make([]Item, 0, len(clusters))
	for _, c := range clusters {
		srcs, err := a.d.Sources.SourcesForCluster(ctx, c.ID)
		if err != nil {
			// Sources are decorative; don't fail the digest if one
			// cluster's sources lookup hiccups — just drop sources for it.
			a.d.Logger.WarnContext(ctx, "sources lookup failed",
				slog.Int64("cluster_id", c.ID), slog.Any("err", err))
			srcs = nil
		}
		items = append(items, Item{
			ClusterID: c.ID,
			Headline:  fallback(c.Headline, "Без заголовка"),
			Summary:   c.Summary,
			Topics:    c.Topics,
			Sources:   srcs,
			Score:     c.Score,
		})
	}

	text := Format(items, FormatOptions{Now: a.d.Now(), Title: req.Title})
	if text == "" {
		return nil
	}

	if err := a.d.Sender.SendDigest(ctx, req.TGUserID, text); err != nil {
		return fmt.Errorf("send: %w", err)
	}
	for _, it := range items {
		if err := a.d.Deliveries.Record(ctx, req.UserID, it.ClusterID, req.Channel); err != nil {
			// Already sent — log but do not return the error, otherwise the
			// caller may retry the send and the user gets the same digest
			// twice. Anti-repeat will be imperfect until the next successful
			// record, but better than a duplicate send.
			a.d.Logger.WarnContext(ctx, "record delivery failed",
				slog.Int64("user_id", req.UserID),
				slog.Int64("cluster_id", it.ClusterID),
				slog.Any("err", err))
		}
	}
	return nil
}

// applyCustomTopicFilter drops clusters whose centroid does not match
// any of userID's custom topics. The filter is intentionally a no-op
// (returns clusters unchanged) when:
//
//   - Any of the Custom*/Tier/Users deps is nil — partial wiring keeps
//     the legacy code paths working without forcing every caller to
//     adopt the Pro filter at once.
//   - The user lookup or tier check rules the recipient as non-Pro —
//     Free users (who cannot add custom topics anyway) see all clusters.
//
// When the filter is active and a cluster has no embeddings yet
// (ErrNoEmbeddings), the cluster passes through — preferring an
// imperfect filter to silently dropping fresh clusters.
//
// INV-5 preserved: the cost paid here is one Postgres round trip per
// cluster (centroid + EXISTS); no LLM call. Custom-topic embeddings are
// computed once on /topics add, never on a digest assembly.
func (a *Assembler) applyCustomTopicFilter(
	ctx context.Context, userID int64, clusters []postgres.Cluster,
) ([]postgres.Cluster, error) {
	if a.d.CustomTopics == nil || a.d.Centroider == nil || a.d.Tier == nil || a.d.Users == nil {
		return clusters, nil
	}
	u, err := a.d.Users.GetByID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("load user: %w", err)
	}
	if !a.d.Tier.IsPro(u) {
		return clusters, nil
	}
	kept := clusters[:0]
	for _, c := range clusters {
		cen, err := a.d.Centroider.ClusterCentroid(ctx, c.ID)
		if err != nil {
			if errors.Is(err, postgres.ErrNoEmbeddings) {
				// No embeddings yet — keep the cluster rather than drop it.
				kept = append(kept, c)
				continue
			}
			a.d.Logger.WarnContext(ctx, "centroid lookup failed",
				slog.Int64("cluster_id", c.ID), slog.Any("err", err))
			continue
		}
		ok, err := a.d.CustomTopics.MatchesAny(ctx, userID, cen)
		if err != nil {
			a.d.Logger.WarnContext(ctx, "custom topic match failed",
				slog.Int64("cluster_id", c.ID), slog.Any("err", err))
			continue
		}
		if !ok {
			continue
		}
		kept = append(kept, c)
	}
	return kept, nil
}

func fallback(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
