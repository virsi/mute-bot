// Package eval provides utilities for loading the golden post corpus and
// running offline evaluation harnesses (precision/recall) against the
// dedup/classifier pipelines.
package eval

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// GoldRecord is one hand-labelled post in the golden corpus.
// Records are stored one-per-line as JSON (.jsonl).
type GoldRecord struct {
	ID            string    `json:"id"`
	Channel       string    `json:"channel"`
	Text          string    `json:"text"`
	PostedAt      time.Time `json:"posted_at"`
	ClusterLabel  string    `json:"cluster_label"`
	SeverityLabel int       `json:"severity_label"`
}

// LoadCorpus reads a JSONL file and returns one GoldRecord per non-blank line.
// Blank lines are skipped so an empty corpus file is a valid input that
// produces a nil/empty slice.
func LoadCorpus(path string) ([]GoldRecord, error) {
	f, err := os.Open(path) //nolint:gosec // path comes from test fixtures
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)

	var out []GoldRecord
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var r GoldRecord
		if err := json.Unmarshal(line, &r); err != nil {
			return nil, fmt.Errorf("line: %w", err)
		}
		out = append(out, r)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan: %w", err)
	}
	return out, nil
}
