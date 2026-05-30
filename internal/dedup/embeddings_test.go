package dedup

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/virsi/mute-bot/internal/llm"
)

type fakeLLM struct {
	calls int
	err   error
}

func (f *fakeLLM) Embed(_ context.Context, req llm.EmbedRequest) (llm.EmbedResponse, error) {
	f.calls++
	if f.err != nil {
		return llm.EmbedResponse{}, f.err
	}
	out := make([][]float32, len(req.Texts))
	for i := range out {
		out[i] = []float32{0.1, 0.2}
	}
	return llm.EmbedResponse{Vectors: out, Model: req.Model}, nil
}

type fakeCache struct {
	data map[[32]byte][]float32
}

func newFakeCache() *fakeCache { return &fakeCache{data: map[[32]byte][]float32{}} }

func (f *fakeCache) Get(_ context.Context, h [32]byte) ([]float32, bool, error) {
	v, ok := f.data[h]
	return v, ok, nil
}

func (f *fakeCache) Set(_ context.Context, h [32]byte, v []float32) error {
	f.data[h] = v
	return nil
}

func TestEmbedder_CacheHit(t *testing.T) {
	c := newFakeCache()
	l := &fakeLLM{}
	e := NewEmbedder(EmbedderDeps{LLM: l, Cache: c, Model: "test"})

	text := "Hello world"
	hash := sha256.Sum256([]byte(text))
	require.NoError(t, c.Set(context.Background(), hash, []float32{0.9, 0.1}))

	v, err := e.EmbedOne(context.Background(), text, hash)
	require.NoError(t, err)
	require.Equal(t, []float32{0.9, 0.1}, v)
	require.Equal(t, 0, l.calls) // cache served
}

func TestEmbedder_CacheMiss_CallsLLM(t *testing.T) {
	c := newFakeCache()
	l := &fakeLLM{}
	e := NewEmbedder(EmbedderDeps{LLM: l, Cache: c, Model: "test"})

	text := "Hello world"
	hash := sha256.Sum256([]byte(text))
	v, err := e.EmbedOne(context.Background(), text, hash)
	require.NoError(t, err)
	require.Len(t, v, 2)
	require.Equal(t, 1, l.calls)
	cached, ok, _ := c.Get(context.Background(), hash)
	require.True(t, ok)
	require.Equal(t, v, cached)
}

func TestEmbedder_LLMError_Propagates(t *testing.T) {
	c := newFakeCache()
	l := &fakeLLM{err: errors.New("boom")}
	e := NewEmbedder(EmbedderDeps{LLM: l, Cache: c, Model: "test"})

	hash := sha256.Sum256([]byte("x"))
	_, err := e.EmbedOne(context.Background(), "x", hash)
	require.Error(t, err)
}
