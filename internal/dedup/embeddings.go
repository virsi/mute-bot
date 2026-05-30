package dedup

import (
	"context"
	"fmt"

	"github.com/virsi/mute-bot/internal/llm"
)

// EmbeddingCache caches text-hash → embedding vectors so re-deliveries of
// the same normalized post (which keeps the same hash) skip the LLM call.
type EmbeddingCache interface {
	Get(ctx context.Context, hash [32]byte) ([]float32, bool, error)
	Set(ctx context.Context, hash [32]byte, v []float32) error
}

// LLMEmbedder is the slice of the LLM provider interface this component
// needs. Narrow on purpose so unit tests can stub it without pulling the
// chat surface.
type LLMEmbedder interface {
	Embed(ctx context.Context, req llm.EmbedRequest) (llm.EmbedResponse, error)
}

// EmbedderDeps groups the Embedder collaborators.
type EmbedderDeps struct {
	LLM   LLMEmbedder
	Cache EmbeddingCache
	Model string
}

// Embedder turns a normalized post into a fixed-dimension embedding vector.
// It checks the cache first; on a miss it calls the LLM and writes through.
type Embedder struct {
	d EmbedderDeps
}

// NewEmbedder constructs an Embedder bound to d.
func NewEmbedder(d EmbedderDeps) *Embedder { return &Embedder{d: d} }

// EmbedOne returns the embedding vector for text. hash must be the SHA-256
// of the canonical (cleaned) text so cache lookups are consistent with the
// upstream normalize stage. Cache write-through errors are tolerated — they
// only hurt latency on the next call, not correctness.
func (e *Embedder) EmbedOne(ctx context.Context, text string, hash [32]byte) ([]float32, error) {
	if v, ok, err := e.d.Cache.Get(ctx, hash); err == nil && ok {
		return v, nil
	}
	resp, err := e.d.LLM.Embed(ctx, llm.EmbedRequest{Texts: []string{text}, Model: e.d.Model})
	if err != nil {
		return nil, fmt.Errorf("llm embed: %w", err)
	}
	if len(resp.Vectors) == 0 {
		return nil, fmt.Errorf("no vector returned")
	}
	v := resp.Vectors[0]
	_ = e.d.Cache.Set(ctx, hash, v)
	return v, nil
}
