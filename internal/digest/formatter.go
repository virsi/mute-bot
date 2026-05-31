// Package digest assembles per-user news digests and renders them as
// Telegram-bound messages. The package is layered top-down: the formatter
// (this file) is a pure function that turns a slice of Item into the final
// text, and the assembler (assembler.go) orchestrates fetching, filtering,
// and sending.
package digest

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/virsi/mute-bot/internal/domain"
)

// Item is the input unit consumed by Format. One Item maps to one cluster
// the user should see in their digest.
type Item struct {
	ClusterID int64
	Headline  string
	Summary   string
	Topics    []string
	Sources   []string
	Score     float32
}

// FormatOptions controls non-content aspects of the rendered digest.
type FormatOptions struct {
	// Now is the timestamp printed in the digest header. Injected to keep
	// Format deterministic and easy to test.
	Now time.Time
	// Title is the human-facing name of the digest run (e.g. "Утренняя сводка").
	Title string
}

// Format renders items as a single Telegram message. Items are grouped by
// their primary topic. Topic blocks appear in the order topics were first
// encountered in items (preserving caller intent rather than imposing
// alphabetical or score ordering on the topic level itself). Inside each
// block, items are sorted by Score descending.
//
// Returns an empty string for an empty input — the assembler uses that as
// the signal to skip the send altogether.
func Format(items []Item, opts FormatOptions) string {
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "🗞 %s · %s\n\n", opts.Title, opts.Now.Format("02.01.2006"))

	byTopic := make(map[string][]Item, len(items))
	order := make([]string, 0, len(items))
	for _, it := range items {
		key := primaryTopic(it.Topics)
		if _, ok := byTopic[key]; !ok {
			order = append(order, key)
		}
		byTopic[key] = append(byTopic[key], it)
	}

	for _, topic := range order {
		fmt.Fprintf(&b, "📌 %s\n", topicTitle(topic))
		topicItems := byTopic[topic]
		sort.SliceStable(topicItems, func(i, j int) bool {
			return topicItems[i].Score > topicItems[j].Score
		})
		for i, it := range topicItems {
			fmt.Fprintf(&b, "%d. %s\n   %s\n   📡 %s\n\n",
				i+1, it.Headline, it.Summary, formatSources(it.Sources))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// primaryTopic returns the first topic for the item, or "other" if none.
func primaryTopic(topics []string) string {
	if len(topics) == 0 {
		return "other"
	}
	return topics[0]
}

// topicTitle maps a topic id to its display title via the domain preset list.
// Falls back to "Прочее" for unknown ids (e.g. "other").
func topicTitle(id string) string {
	for _, t := range domain.PresetTopics {
		if t.ID == id {
			return t.Title
		}
	}
	return "Прочее"
}

// formatSources renders the source channel list as a comma-separated list of
// @-prefixed usernames, capped at 5 entries. An empty list renders as "—".
func formatSources(s []string) string {
	if len(s) == 0 {
		return "—"
	}
	const maxSources = 5
	out := make([]string, 0, len(s))
	for i, u := range s {
		if i >= maxSources {
			break
		}
		out = append(out, "@"+u)
	}
	return strings.Join(out, ", ")
}
