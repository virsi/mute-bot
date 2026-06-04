package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenAI_Embed_HitsMockServer(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/embeddings", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"embedding": []float32{0.1, 0.2, 0.3}, "index": 0},
			},
			"model": "text-embedding-3-small",
			"usage": map[string]int{"total_tokens": 5},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	bg := NewBudgetGuard(BudgetConfig{MonthlyUSD: 1.0})
	o := NewOpenAI(OpenAIConfig{APIKey: "k", BaseURL: srv.URL, Budget: bg})
	resp, err := o.Embed(context.Background(), EmbedRequest{Texts: []string{"hi"}, Model: "text-embedding-3-small"})
	require.NoError(t, err)
	require.Len(t, resp.Vectors, 1)
	require.Equal(t, []float32{0.1, 0.2, 0.3}, resp.Vectors[0])
}

func TestOpenAI_Embed_RespectsBudget(t *testing.T) {
	bg := NewBudgetGuard(BudgetConfig{MonthlyUSD: 0.0000001})
	_ = bg.Charge(context.Background(), 1.0) // exhaust
	o := NewOpenAI(OpenAIConfig{APIKey: "k", BaseURL: "http://invalid.local", Budget: bg})
	_, err := o.Embed(context.Background(), EmbedRequest{Texts: []string{"hi"}})
	require.ErrorIs(t, err, ErrBudgetExceeded)
}
