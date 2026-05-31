package dedup

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/virsi/mute-bot/internal/llm"
)

type stubLLM struct {
	resp string
	err  error
}

func (s *stubLLM) Chat(_ context.Context, _ llm.ChatRequest) (llm.ChatResponse, error) {
	if s.err != nil {
		return llm.ChatResponse{}, s.err
	}
	return llm.ChatResponse{Text: s.resp}, nil
}

func TestLLMJudge_DecidesSame(t *testing.T) {
	j := NewLLMJudge(LLMJudgeDeps{
		LLM:   &stubLLM{resp: `{"same":true,"confidence":0.92}`},
		Model: "gpt-4o-mini",
	})
	same, conf, err := j.Decide(context.Background(), "Putin signed law", "President signed the law today")
	require.NoError(t, err)
	require.True(t, same)
	require.InDelta(t, 0.92, conf, 0.001)
}

func TestLLMJudge_DecidesDifferent(t *testing.T) {
	j := NewLLMJudge(LLMJudgeDeps{
		LLM:   &stubLLM{resp: `{"same":false,"confidence":0.85}`},
		Model: "gpt-4o-mini",
	})
	same, conf, err := j.Decide(context.Background(), "Bitcoin crashed", "New iPhone launched")
	require.NoError(t, err)
	require.False(t, same)
	require.InDelta(t, 0.85, conf, 0.001)
}

func TestLLMJudge_LLMError_Propagates(t *testing.T) {
	j := NewLLMJudge(LLMJudgeDeps{
		LLM:   &stubLLM{err: errors.New("api down")},
		Model: "gpt-4o-mini",
	})
	_, _, err := j.Decide(context.Background(), "a", "b")
	require.Error(t, err)
}

func TestLLMJudge_RejectsGarbageResponse(t *testing.T) {
	j := NewLLMJudge(LLMJudgeDeps{
		LLM:   &stubLLM{resp: "not json"},
		Model: "gpt-4o-mini",
	})
	_, _, err := j.Decide(context.Background(), "a", "b")
	require.Error(t, err)
}
