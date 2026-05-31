package dedup

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/virsi/mute-bot/internal/normalize"
)

type stubMatcher struct {
	called bool
	in     MatchInput
	err    error
}

func (s *stubMatcher) Match(_ context.Context, in MatchInput) error {
	s.called = true
	s.in = in
	return s.err
}

func TestWorker_DelegatesToMatcher(t *testing.T) {
	m := &stubMatcher{}
	w := NewWorker(WorkerDeps{Matcher: m})
	hash := [32]byte{9, 8, 7}
	evt := normalize.NormalizedPostEvent{
		PostID:    9,
		ChannelID: 1,
		TextClean: "hello",
		TextHash:  hash,
		Lang:      "en",
		PostedAt:  time.Now().UTC(),
	}
	data, err := json.Marshal(evt)
	require.NoError(t, err)
	require.NoError(t, w.Handle(context.Background(), data))
	require.True(t, m.called)
	require.Equal(t, int64(9), m.in.PostID)
	require.Equal(t, "hello", m.in.TextClean)
	require.Equal(t, hash, m.in.Hash)
}

func TestWorker_PropagatesMatcherError(t *testing.T) {
	m := &stubMatcher{err: errors.New("boom")}
	w := NewWorker(WorkerDeps{Matcher: m})
	evt := normalize.NormalizedPostEvent{PostID: 1, TextClean: "x"}
	data, _ := json.Marshal(evt)
	require.Error(t, w.Handle(context.Background(), data))
}

func TestWorker_RejectsInvalidJSON(t *testing.T) {
	w := NewWorker(WorkerDeps{Matcher: &stubMatcher{}})
	err := w.Handle(context.Background(), []byte("not json"))
	require.Error(t, err)
}
