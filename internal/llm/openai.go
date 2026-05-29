package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type OpenAIConfig struct {
	APIKey  string
	BaseURL string // default https://api.openai.com
	Budget  *BudgetGuard
	HTTP    *http.Client
}

type OpenAI struct {
	cfg OpenAIConfig
}

// Compile-time check that *OpenAI satisfies the Provider interface.
var _ Provider = (*OpenAI)(nil)

func NewOpenAI(cfg OpenAIConfig) *OpenAI {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.openai.com"
	}
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{Timeout: 30 * time.Second}
	}
	return &OpenAI{cfg: cfg}
}

// pricing per 1M tokens (June 2026 baseline — tune in config)
const (
	embedSmallUSDPerMillion = 0.02
	chatMiniInputUSDPerM    = 0.15
	chatMiniOutputUSDPerM   = 0.60
)

func (o *OpenAI) Embed(ctx context.Context, req EmbedRequest) (EmbedResponse, error) {
	cost := embedSmallUSDPerMillion * float64(estimateTokens(req.Texts)) / 1_000_000
	if err := o.cfg.Budget.Charge(ctx, cost); err != nil {
		return EmbedResponse{}, err
	}
	body, err := json.Marshal(map[string]any{
		"input": req.Texts,
		"model": req.Model,
	})
	if err != nil {
		return EmbedResponse{}, fmt.Errorf("marshal embed request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, o.cfg.BaseURL+"/v1/embeddings", bytes.NewReader(body))
	if err != nil {
		return EmbedResponse{}, fmt.Errorf("build embed request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+o.cfg.APIKey)
	httpReq.Header.Set("Content-Type", "application/json")

	res, err := o.cfg.HTTP.Do(httpReq)
	if err != nil {
		return EmbedResponse{}, fmt.Errorf("embed http: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		raw, _ := io.ReadAll(res.Body)
		return EmbedResponse{}, fmt.Errorf("openai embed %d: %s", res.StatusCode, string(raw))
	}
	var parsed struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
		Model string `json:"model"`
		Usage struct {
			TotalTokens int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(res.Body).Decode(&parsed); err != nil {
		return EmbedResponse{}, fmt.Errorf("decode embed: %w", err)
	}
	out := EmbedResponse{
		Vectors: make([][]float32, len(parsed.Data)),
		Model:   parsed.Model,
		Tokens:  parsed.Usage.TotalTokens,
	}
	for i, d := range parsed.Data {
		out.Vectors[i] = d.Embedding
	}
	return out, nil
}

func (o *OpenAI) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	estIn := estimateTokensOne(req.System) + estimateTokensOne(req.User)
	estOut := req.MaxTokens
	cost := chatMiniInputUSDPerM*float64(estIn)/1_000_000 + chatMiniOutputUSDPerM*float64(estOut)/1_000_000
	if err := o.cfg.Budget.Charge(ctx, cost); err != nil {
		return ChatResponse{}, err
	}

	body, err := json.Marshal(map[string]any{
		"model": req.Model,
		"messages": []map[string]string{
			{"role": "system", "content": req.System},
			{"role": "user", "content": req.User},
		},
		"max_tokens":  req.MaxTokens,
		"temperature": 0.2,
	})
	if err != nil {
		return ChatResponse{}, fmt.Errorf("marshal chat request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, o.cfg.BaseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return ChatResponse{}, fmt.Errorf("build chat request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+o.cfg.APIKey)
	httpReq.Header.Set("Content-Type", "application/json")

	res, err := o.cfg.HTTP.Do(httpReq)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("chat http: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		raw, _ := io.ReadAll(res.Body)
		return ChatResponse{}, fmt.Errorf("openai chat %d: %s", res.StatusCode, string(raw))
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(res.Body).Decode(&parsed); err != nil {
		return ChatResponse{}, fmt.Errorf("decode chat: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return ChatResponse{}, fmt.Errorf("openai chat: no choices in response")
	}
	return ChatResponse{
		Text:         parsed.Choices[0].Message.Content,
		InputTokens:  parsed.Usage.PromptTokens,
		OutputTokens: parsed.Usage.CompletionTokens,
	}, nil
}

func estimateTokens(texts []string) int {
	t := 0
	for _, s := range texts {
		t += estimateTokensOne(s)
	}
	return t
}

func estimateTokensOne(s string) int {
	// rough 4 chars/token heuristic; replaced when SDK gives exact usage
	if len(s) == 0 {
		return 0
	}
	return (len(s) + 3) / 4
}
