package llm

import "context"

type EmbedRequest struct {
	Texts []string
	Model string
}

type EmbedResponse struct {
	Vectors [][]float32
	Model   string
	Tokens  int
}

type ChatRequest struct {
	Model      string
	System     string
	User       string
	JSONSchema string // optional, when provider supports structured output
	MaxTokens  int
}

type ChatResponse struct {
	Text         string
	InputTokens  int
	OutputTokens int
}

type Provider interface {
	Embed(ctx context.Context, req EmbedRequest) (EmbedResponse, error)
	Chat(ctx context.Context, req ChatRequest) (ChatResponse, error)
}
