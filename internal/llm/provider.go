package llm

import "context"

// EmbedRequest is the input to Provider.Embed.
type EmbedRequest struct {
	Texts []string
	Model string
}

// EmbedResponse is the output of Provider.Embed.
type EmbedResponse struct {
	Vectors [][]float32
	Model   string
	Tokens  int
}

// ChatRequest is the input to Provider.Chat.
type ChatRequest struct {
	Model      string
	System     string
	User       string
	JSONSchema string // optional, when provider supports structured output
	MaxTokens  int
}

// ChatResponse is the output of Provider.Chat.
type ChatResponse struct {
	Text         string
	InputTokens  int
	OutputTokens int
}

// Provider is the LLM surface used by classifiers, ranker, and dedup judge.
type Provider interface {
	Embed(ctx context.Context, req EmbedRequest) (EmbedResponse, error)
	Chat(ctx context.Context, req ChatRequest) (ChatResponse, error)
}
