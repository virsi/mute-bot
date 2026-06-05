package digest

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/virsi/mute-bot/internal/storage/postgres"
)

// WeeklyTopReader is the slice of ClustersRepo the weekly assembler uses.
type WeeklyTopReader interface {
	TopByScoreSince(ctx context.Context, since time.Time,
		topics []string, exclude []int64, limit int) ([]postgres.Cluster, error)
}

// WeeklyRepo is the slice of WeeklyDeliveriesRepo the assembler needs.
type WeeklyRepo interface {
	InsertIfAbsent(ctx context.Context, userID, clusterID int64, isoWeek string) (bool, error)
	HasWeekRow(ctx context.Context, userID int64, isoWeek string) (bool, error)
	ListClusterIDsSince(ctx context.Context, userID int64, since time.Time, limit int) ([]int64, error)
}

// WeeklyAssemblerDeps wires WeeklyAssembler to its collaborators. Custom-
// topic filter ports (Tier, Users, Centroider, CustomTopics) are optional
// and behave identically to the daily Assembler: when wired and the user
// is Pro with custom topics, the per-cluster centroid filter trims the
// top-N down to topical matches; otherwise the top-N is sent as-is.
type WeeklyAssemblerDeps struct {
	Settings SettingsReader
	Clusters WeeklyTopReader
	Weekly   WeeklyRepo
	Sources  SourcesReader
	Sender   Sender

	Tier         TierChecker
	Users        UserLoader
	CustomTopics TopicMatcher
	Centroider   Centroider

	// Now is injectable for tests; defaults to time.Now.
	Now func() time.Time
	// LookbackDays defaults to 7.
	LookbackDays int
	// MaxItems defaults to 10.
	MaxItems int
	// Title overrides the default ru-RU header (the date range is appended
	// in parentheses).
	Title string
	// SkipAntiRepeat — when true, the assembler ignores HasWeekRow and does
	// NOT record InsertIfAbsent. Used by the on-demand /weekly command so a
	// Pro user can re-pull the digest as many times as they want without
	// burning the cron's once-per-week anti-repeat. The cron path keeps
	// this false so Sunday-18:00 fan-out stays idempotent.
	SkipAntiRepeat bool
	// Logger defaults to slog.Default().
	Logger *slog.Logger
}

// WeeklyAssembler builds the Sunday-18:00 weekly digest for one Pro user.
// Anti-repeat: a user already served in the current ISO week is a no-op;
// clusters seen in any past week's digest within the look-back window are
// excluded from the top-N query.
type WeeklyAssembler struct{ d WeeklyAssemblerDeps }

// NewWeeklyAssembler constructs the assembler with sensible defaults.
func NewWeeklyAssembler(d WeeklyAssemblerDeps) *WeeklyAssembler {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.LookbackDays == 0 {
		d.LookbackDays = 7
	}
	if d.MaxItems == 0 {
		d.MaxItems = 10
	}
	if d.Title == "" {
		d.Title = "Главное за неделю"
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	return &WeeklyAssembler{d: d}
}

// WeeklyRequest carries the per-call parameters.
type WeeklyRequest struct {
	UserID   int64
	TGUserID int64
}

// BuildWeekly assembles, sends, and records the weekly digest for the
// requested user. Returns nil when there is nothing to send (no clusters
// or the user has already received this week's digest via the cron path
// — on-demand /weekly bypasses HasWeekRow via SkipAntiRepeat).
func (a *WeeklyAssembler) BuildWeekly(ctx context.Context, req WeeklyRequest) error {
	now := a.d.Now()
	isoWeek := ISOWeekKey(now)

	if !a.d.SkipAntiRepeat {
		has, err := a.d.Weekly.HasWeekRow(ctx, req.UserID, isoWeek)
		if err != nil {
			return fmt.Errorf("has weekly row: %w", err)
		}
		if has {
			a.d.Logger.InfoContext(ctx, "weekly: already sent this iso week",
				slog.Int64("user_id", req.UserID), slog.String("iso_week", isoWeek))
			return nil
		}
	}

	s, err := a.d.Settings.Get(ctx, req.UserID)
	if err != nil {
		return fmt.Errorf("settings: %w", err)
	}

	since := now.Add(-time.Duration(a.d.LookbackDays) * 24 * time.Hour)
	excluded, err := a.d.Weekly.ListClusterIDsSince(ctx, req.UserID, since, 200)
	if err != nil {
		return fmt.Errorf("excluded: %w", err)
	}

	clusters, err := a.d.Clusters.TopByScoreSince(ctx, since, s.Topics, excluded, a.d.MaxItems)
	if err != nil {
		return fmt.Errorf("top by score: %w", err)
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
		srcs, srcErr := a.d.Sources.SourcesForCluster(ctx, c.ID)
		if srcErr != nil {
			a.d.Logger.WarnContext(ctx, "weekly: sources lookup",
				slog.Int64("cluster_id", c.ID), slog.Any("err", srcErr))
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

	rangeLabel := fmt.Sprintf("%s — %s",
		since.Format("02.01"), now.Format("02.01.2006"))
	title := a.d.Title + " (" + rangeLabel + ")"
	text := Format(items, FormatOptions{Now: now, Title: title})
	if text == "" {
		return nil
	}
	if err := a.d.Sender.SendDigest(ctx, req.TGUserID, text); err != nil {
		return fmt.Errorf("send: %w", err)
	}
	if a.d.SkipAntiRepeat {
		return nil
	}
	for _, it := range items {
		if _, err := a.d.Weekly.InsertIfAbsent(ctx, req.UserID, it.ClusterID, isoWeek); err != nil {
			a.d.Logger.WarnContext(ctx, "weekly: record delivery",
				slog.Int64("user_id", req.UserID),
				slog.Int64("cluster_id", it.ClusterID),
				slog.String("iso_week", isoWeek),
				slog.Any("err", err))
		}
	}
	return nil
}

// applyCustomTopicFilter mirrors Assembler.applyCustomTopicFilter so Pro
// users with custom topics see the same per-cluster centroid gate on the
// weekly digest as on the daily one. Nil-safe — if any of the Tier/Users/
// CustomTopics/Centroider deps is nil, the filter is a passthrough.
func (a *WeeklyAssembler) applyCustomTopicFilter(
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
				kept = append(kept, c)
				continue
			}
			a.d.Logger.WarnContext(ctx, "weekly: centroid",
				slog.Int64("cluster_id", c.ID), slog.Any("err", err))
			continue
		}
		ok, mErr := a.d.CustomTopics.MatchesAny(ctx, userID, cen)
		if mErr != nil {
			a.d.Logger.WarnContext(ctx, "weekly: match",
				slog.Int64("cluster_id", c.ID), slog.Any("err", mErr))
			continue
		}
		if !ok {
			continue
		}
		kept = append(kept, c)
	}
	return kept, nil
}

// ISOWeekKey returns the "YYYY-WW" formatted ISO-8601 week key used as the
// anti-repeat partition. time.ISOWeek() returns (year, week 1..53) which
// we format with two zero-padded digits.
func ISOWeekKey(t time.Time) string {
	y, w := t.ISOWeek()
	return fmt.Sprintf("%04d-%02d", y, w)
}
