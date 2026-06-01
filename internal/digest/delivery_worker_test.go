package digest

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// stubAssemblerForWorker records each Assemble invocation so tests can
// assert the worker translated the JetStream payload into the right
// AssembleRequest fields.
type stubAssemblerForWorker struct {
	calls []AssembleRequest
	err   error
}

func (s *stubAssemblerForWorker) Assemble(_ context.Context, r AssembleRequest) error {
	s.calls = append(s.calls, r)
	return s.err
}

func TestDeliveryWorker_DispatchesDigest(t *testing.T) {
	a := &stubAssemblerForWorker{}
	w := NewDeliveryWorker(a)

	data, err := json.Marshal(map[string]any{
		"user_id":    7,
		"tg_user_id": 100,
		"channel":    "digest",
	})
	require.NoError(t, err)

	require.NoError(t, w.Handle(context.Background(), data))
	require.Len(t, a.calls, 1)
	require.Equal(t, int64(7), a.calls[0].UserID)
	require.Equal(t, int64(100), a.calls[0].TGUserID)
	require.Equal(t, "digest", a.calls[0].Channel)
	require.Equal(t, "Утренняя сводка", a.calls[0].Title)
}

func TestDeliveryWorker_WeeklyTitle(t *testing.T) {
	a := &stubAssemblerForWorker{}
	w := NewDeliveryWorker(a)

	data, _ := json.Marshal(map[string]any{
		"user_id":    7,
		"tg_user_id": 100,
		"channel":    "weekly",
	})
	require.NoError(t, w.Handle(context.Background(), data))
	require.Equal(t, "Недельная сводка", a.calls[0].Title)
}

func TestDeliveryWorker_PropagatesAssemblerError(t *testing.T) {
	want := errors.New("boom")
	a := &stubAssemblerForWorker{err: want}
	w := NewDeliveryWorker(a)

	data, _ := json.Marshal(map[string]any{"user_id": 1, "tg_user_id": 2, "channel": "digest"})
	err := w.Handle(context.Background(), data)
	require.ErrorIs(t, err, want)
}

func TestDeliveryWorker_RejectsMalformedJSON(t *testing.T) {
	w := NewDeliveryWorker(&stubAssemblerForWorker{})
	err := w.Handle(context.Background(), []byte("not json"))
	require.Error(t, err)
}
