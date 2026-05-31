package digest

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/virsi/mute-bot/internal/storage/postgres"
)

// SettingsReader returns the per-user delivery settings.
type SettingsReader interface {
	Get(ctx context.Context, userID int64) (postgres.Settings, error)
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

// AssemblerDeps wires the assembler to its collaborators. All fields except
// Now/HistoryWindow/MaxItems are required.
type AssemblerDeps struct {
	Settings   SettingsReader
	Clusters   ClustersSearcher
	Deliveries DeliveriesRW
	Sources    SourcesReader
	Sender     Sender

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

func fallback(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
