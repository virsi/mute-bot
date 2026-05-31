// Package classify maps news clusters to preset topics and ranks their
// importance. The classifier calls an LLM with a deterministic prompt and a
// parsed JSON response; the worker debounces cluster.updated events so the
// cluster has time to accrete posts before being classified.
package classify

import (
	"strings"

	"github.com/virsi/mute-bot/internal/domain"
)

// PresetsList renders domain.PresetTopics as a bullet list suitable for
// substitution into the classifier prompt. The format is stable so prompt
// snapshots are reproducible.
func PresetsList() string {
	var b strings.Builder
	for _, t := range domain.PresetTopics {
		b.WriteString("- ")
		b.WriteString(t.ID)
		b.WriteString(": ")
		b.WriteString(t.Description)
		b.WriteString("\n")
	}
	return b.String()
}
