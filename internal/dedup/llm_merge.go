package dedup

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/virsi/mute-bot/internal/llm"
)

// LLMChat is the slice of llm.Provider the judge uses. Narrow on purpose so
// the judge can be swapped or tested without the embedding surface.
type LLMChat interface {
	Chat(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error)
}

// LLMJudgeDeps groups the judge's collaborators.
type LLMJudgeDeps struct {
	LLM   LLMChat
	Model string
}

// LLMJudge asks the model whether two short news snippets describe the same
// real-world event. It is invoked from the borderline-pair queue (cosine
// distance in [MaxDistance, MaxDistance+0.1]) by a periodic reconciliation
// job — not from the hot path in Matcher.Match — so latency is not
// performance-critical.
type LLMJudge struct {
	d LLMJudgeDeps
}

// NewLLMJudge constructs a judge bound to d. The Model field is mandatory
// for any non-test caller; the constructor does not fill a default to keep
// model selection an explicit decision.
func NewLLMJudge(d LLMJudgeDeps) *LLMJudge { return &LLMJudge{d: d} }

// judgePrompt is intentionally short and explicit about the output shape so
// the model rarely strays into prose. Phase 1 uses plain JSON output; once
// we move to a structured-output provider this prompt can be replaced with
// a schema reference.
const judgePrompt = `You are a news deduplication classifier. Decide if two short news snippets describe the same real-world event.
Reply with JSON only: {"same": bool, "confidence": float in [0,1]}.
No prose, no markdown.`

// Decide returns whether snippets a and b describe the same event, along
// with the model's reported confidence. Malformed or non-JSON responses are
// surfaced as errors — callers must decide whether to retry or skip.
func (j *LLMJudge) Decide(ctx context.Context, a, b string) (bool, float64, error) {
	user := fmt.Sprintf("Snippet A:\n%s\n\nSnippet B:\n%s", a, b)
	resp, err := j.d.LLM.Chat(ctx, llm.ChatRequest{
		Model:     j.d.Model,
		System:    judgePrompt,
		User:      user,
		MaxTokens: 50,
	})
	if err != nil {
		return false, 0, fmt.Errorf("llm chat: %w", err)
	}
	var parsed struct {
		Same       bool    `json:"same"`
		Confidence float64 `json:"confidence"`
	}
	if err := json.Unmarshal([]byte(resp.Text), &parsed); err != nil {
		return false, 0, fmt.Errorf("parse judge response %q: %w", resp.Text, err)
	}
	return parsed.Same, parsed.Confidence, nil
}
