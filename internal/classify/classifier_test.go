package classify

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/virsi/mute-bot/internal/llm"
)

type fakeLLM struct {
	resp        string
	err         error
	lastRequest llm.ChatRequest
}

func (f *fakeLLM) Chat(_ context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	f.lastRequest = req
	if f.err != nil {
		return llm.ChatResponse{}, f.err
	}
	return llm.ChatResponse{Text: f.resp}, nil
}

func TestClassifier_Parses(t *testing.T) {
	lm := &fakeLLM{resp: `{"topics":["politics","economy"],"severity":75,"headline":"H","summary":"S."}`}
	c := NewClassifier(ClassifierDeps{LLM: lm, Model: "gpt-4o-mini"})

	res, err := c.Classify(context.Background(), []string{"post1", "post2"}, "ru")
	require.NoError(t, err)
	require.Equal(t, []string{"politics", "economy"}, res.Topics)
	require.Equal(t, 75, res.Severity)
	require.Equal(t, "H", res.Headline)
	require.Equal(t, "S.", res.Summary)

	// Prompt should be fully expanded — no leftover placeholders.
	require.NotContains(t, lm.lastRequest.User, "{{TOPICS_LIST}}")
	require.NotContains(t, lm.lastRequest.User, "{{LANG}}")
	require.NotContains(t, lm.lastRequest.User, "{{POSTS}}")
	require.Contains(t, lm.lastRequest.User, "ru")
	require.Contains(t, lm.lastRequest.User, "post1")
	require.Equal(t, "gpt-4o-mini", lm.lastRequest.Model)
}

func TestClassifier_InvalidJSON_FallsBack(t *testing.T) {
	c := NewClassifier(ClassifierDeps{
		LLM:   &fakeLLM{resp: "not json"},
		Model: "gpt-4o-mini",
	})

	res, err := c.Classify(context.Background(), []string{"x"}, "ru")
	require.NoError(t, err)
	require.Empty(t, res.Topics)
	require.Equal(t, 0, res.Severity)
	require.Empty(t, res.Headline)
}

func TestClassifier_DefaultsLangToRu(t *testing.T) {
	lm := &fakeLLM{resp: `{"topics":["it"],"severity":50,"headline":"h","summary":"s"}`}
	c := NewClassifier(ClassifierDeps{LLM: lm, Model: "m"})

	_, err := c.Classify(context.Background(), []string{"x"}, "")
	require.NoError(t, err)
	require.Contains(t, lm.lastRequest.User, "summary in ru")
}

func TestClassifier_ClampsSeverity(t *testing.T) {
	tests := []struct {
		name string
		resp string
		want int
	}{
		{"above 100", `{"topics":[],"severity":150,"headline":"","summary":""}`, 100},
		{"below 0", `{"topics":[],"severity":-10,"headline":"","summary":""}`, 0},
		{"within range", `{"topics":[],"severity":42,"headline":"","summary":""}`, 42},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := NewClassifier(ClassifierDeps{LLM: &fakeLLM{resp: tc.resp}, Model: "m"})
			res, err := c.Classify(context.Background(), []string{"x"}, "ru")
			require.NoError(t, err)
			require.Equal(t, tc.want, res.Severity)
		})
	}
}

func TestClassifier_PropagatesLLMError(t *testing.T) {
	c := NewClassifier(ClassifierDeps{
		LLM:   &fakeLLM{err: errors.New("boom")},
		Model: "m",
	})
	_, err := c.Classify(context.Background(), []string{"x"}, "ru")
	require.Error(t, err)
	require.ErrorContains(t, err, "llm chat")
}

func TestClassifier_CapsPostsAtFive(t *testing.T) {
	lm := &fakeLLM{resp: `{"topics":[],"severity":0,"headline":"","summary":""}`}
	c := NewClassifier(ClassifierDeps{LLM: lm, Model: "m"})

	_, err := c.Classify(context.Background(),
		[]string{"alpha", "beta", "gamma", "delta", "epsilon", "zeta", "eta"}, "ru")
	require.NoError(t, err)
	require.Contains(t, lm.lastRequest.User, "[5] epsilon")
	require.NotContains(t, lm.lastRequest.User, "[6]")
	require.NotContains(t, lm.lastRequest.User, "zeta")
	require.NotContains(t, lm.lastRequest.User, " eta")
}

func TestPresetsList_StableOrder(t *testing.T) {
	out := PresetsList()
	require.Contains(t, out, "politics:")
	require.Contains(t, out, "it:")
	// Each topic on its own bullet.
	require.Contains(t, out, "- politics: ")
}
