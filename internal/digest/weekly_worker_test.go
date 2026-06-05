package digest

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

type fakeWeeklyAssembler struct {
	called bool
	req    WeeklyRequest
	err    error
}

func (f *fakeWeeklyAssembler) BuildWeekly(_ context.Context, r WeeklyRequest) error {
	f.called = true
	f.req = r
	return f.err
}

func TestWeeklyWorker_Handle_DispatchesToAssembler(t *testing.T) {
	f := &fakeWeeklyAssembler{}
	w := NewWeeklyWorker(f)
	require.NoError(t, w.Handle(context.Background(), []byte(`{"user_id":1,"tg_user_id":100}`)))
	require.True(t, f.called)
	require.Equal(t, int64(1), f.req.UserID)
	require.Equal(t, int64(100), f.req.TGUserID)
}

func TestWeeklyWorker_Handle_BadJSON_ReturnsErr(t *testing.T) {
	w := NewWeeklyWorker(&fakeWeeklyAssembler{})
	require.Error(t, w.Handle(context.Background(), []byte(`not json`)))
}

func TestWeeklyWorker_Handle_AssemblerErrorPropagates(t *testing.T) {
	f := &fakeWeeklyAssembler{err: errors.New("boom")}
	w := NewWeeklyWorker(f)
	require.Error(t, w.Handle(context.Background(), []byte(`{"user_id":1,"tg_user_id":100}`)))
}
