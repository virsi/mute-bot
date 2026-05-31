package classify

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/virsi/mute-bot/internal/llm"
)

//go:embed prompts/classifier.v1.txt
var promptTemplate string

// LLMChat is the slice of llm.Provider this package needs. Embed lives on the
// dedup side; the classifier only sends a single chat completion per cluster.
type LLMChat interface {
	Chat(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error)
}

// ClassifierDeps groups the Classifier's collaborators.
type ClassifierDeps struct {
	LLM   LLMChat
	Model string
}

// Classifier produces topic, severity, headline and summary for a cluster of
// posts by prompting the LLM with a fixed template.
type Classifier struct {
	d ClassifierDeps
}

// NewClassifier constructs a Classifier bound to d.
func NewClassifier(d ClassifierDeps) *Classifier { return &Classifier{d: d} }

// Result is the parsed classifier output.
type Result struct {
	Topics   []string `json:"topics"`
	Severity int      `json:"severity"`
	Headline string   `json:"headline"`
	Summary  string   `json:"summary"`
}

// Classify renders the prompt for the given posts and language and parses the
// LLM response. Invalid JSON is treated as a soft failure: the method returns
// a zero Result without error so the caller can fall back to "other".
func (c *Classifier) Classify(ctx context.Context, posts []string, lang string) (Result, error) {
	if lang == "" {
		lang = "ru"
	}
	body := strings.ReplaceAll(promptTemplate, "{{TOPICS_LIST}}", PresetsList())
	body = strings.ReplaceAll(body, "{{LANG}}", lang)
	body = strings.ReplaceAll(body, "{{POSTS}}", joinPosts(posts))

	resp, err := c.d.LLM.Chat(ctx, llm.ChatRequest{
		Model:     c.d.Model,
		System:    "Output JSON only.",
		User:      body,
		MaxTokens: 400,
	})
	if err != nil {
		return Result{}, fmt.Errorf("llm chat: %w", err)
	}

	var r Result
	if err := json.Unmarshal([]byte(strings.TrimSpace(resp.Text)), &r); err != nil {
		// Graceful fallback — caller treats empty Topics as "Other".
		return Result{}, nil
	}
	if r.Severity < 0 {
		r.Severity = 0
	}
	if r.Severity > 100 {
		r.Severity = 100
	}
	return r, nil
}

// joinPosts renders up to 5 posts as a numbered list for the prompt body.
// The cap keeps the prompt within model context windows for large clusters.
func joinPosts(p []string) string {
	var b strings.Builder
	for i, s := range p {
		if i >= 5 {
			break
		}
		fmt.Fprintf(&b, "[%d] %s\n", i+1, s)
	}
	return b.String()
}
