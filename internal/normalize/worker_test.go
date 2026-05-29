package normalize

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/virsi/mute-bot/internal/domain"
	"github.com/virsi/mute-bot/internal/queue"
)

type fakePublisher struct {
	subject   string
	published []NormalizedPostEvent
	err       error
}

func (f *fakePublisher) Publish(_ context.Context, subject string, payload any) error {
	if f.err != nil {
		return f.err
	}
	f.subject = subject
	evt, ok := payload.(NormalizedPostEvent)
	if !ok {
		return errors.New("unexpected payload type")
	}
	f.published = append(f.published, evt)
	return nil
}

type fakePosts struct {
	saved []NormalizedPostInsert
	ids   int64
	err   error
}

func (f *fakePosts) Insert(_ context.Context, p NormalizedPostInsert) (int64, error) {
	if f.err != nil {
		return 0, f.err
	}
	f.ids++
	f.saved = append(f.saved, p)
	return f.ids, nil
}

type fakeChannels struct {
	calls []int64
	err   error
}

func (f *fakeChannels) ResolveOrCreate(_ context.Context, tgID int64) (int64, error) {
	if f.err != nil {
		return 0, f.err
	}
	f.calls = append(f.calls, tgID)
	return tgID + 1000, nil
}

func TestHandle_NormalizesAndPublishes(t *testing.T) {
	pub := &fakePublisher{}
	posts := &fakePosts{}
	ch := &fakeChannels{}
	w := NewWorker(WorkerDeps{Publisher: pub, Posts: posts, Channels: ch})

	postedAt := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	raw := domain.RawPost{ChannelID: 100, TGMsgID: 5, Text: "🔥 Hello https://x", PostedAt: postedAt}
	data, err := json.Marshal(raw)
	require.NoError(t, err)

	require.NoError(t, w.Handle(context.Background(), data))

	require.Equal(t, []int64{100}, ch.calls)
	require.Len(t, posts.saved, 1)
	require.Equal(t, "Hello", posts.saved[0].TextClean)
	require.Equal(t, int64(1100), posts.saved[0].ChannelID)
	require.Equal(t, int64(5), posts.saved[0].TGMsgID)
	require.Equal(t, "en", posts.saved[0].Lang)
	require.Equal(t, postedAt, posts.saved[0].PostedAt)

	require.Equal(t, queue.SubjectNormalized, pub.subject)
	require.Len(t, pub.published, 1)
	evt := pub.published[0]
	require.Equal(t, int64(1), evt.PostID)
	require.Equal(t, int64(1100), evt.ChannelID)
	require.Equal(t, "Hello", evt.TextClean)
	require.Equal(t, "en", evt.Lang)
	require.Equal(t, postedAt, evt.PostedAt)
	require.Equal(t, posts.saved[0].TextHash, evt.TextHash)
}

func TestHandle_DropsEmptyAfterClean(t *testing.T) {
	pub := &fakePublisher{}
	posts := &fakePosts{}
	ch := &fakeChannels{}
	w := NewWorker(WorkerDeps{Publisher: pub, Posts: posts, Channels: ch})

	raw := domain.RawPost{ChannelID: 100, TGMsgID: 6, Text: "🔥🔥🔥 https://x @user #tag"}
	data, err := json.Marshal(raw)
	require.NoError(t, err)

	require.NoError(t, w.Handle(context.Background(), data))

	require.Empty(t, posts.saved)
	require.Empty(t, pub.published)
	require.Empty(t, ch.calls)
}

func TestHandle_DetectsRussian(t *testing.T) {
	pub := &fakePublisher{}
	posts := &fakePosts{}
	ch := &fakeChannels{}
	w := NewWorker(WorkerDeps{Publisher: pub, Posts: posts, Channels: ch})

	raw := domain.RawPost{ChannelID: 100, TGMsgID: 7, Text: "Президент подписал указ"}
	data, err := json.Marshal(raw)
	require.NoError(t, err)

	require.NoError(t, w.Handle(context.Background(), data))
	require.Len(t, posts.saved, 1)
	require.Equal(t, "ru", posts.saved[0].Lang)
}

func TestHandle_InvalidJSON(t *testing.T) {
	w := NewWorker(WorkerDeps{Publisher: &fakePublisher{}, Posts: &fakePosts{}, Channels: &fakeChannels{}})
	err := w.Handle(context.Background(), []byte("not-json"))
	require.Error(t, err)
}

func TestHandle_ChannelResolveError(t *testing.T) {
	pub := &fakePublisher{}
	posts := &fakePosts{}
	ch := &fakeChannels{err: errors.New("db down")}
	w := NewWorker(WorkerDeps{Publisher: pub, Posts: posts, Channels: ch})

	raw := domain.RawPost{ChannelID: 100, TGMsgID: 8, Text: "Hello world"}
	data, _ := json.Marshal(raw)

	err := w.Handle(context.Background(), data)
	require.Error(t, err)
	require.Empty(t, posts.saved)
	require.Empty(t, pub.published)
}

func TestHandle_PostsInsertError(t *testing.T) {
	pub := &fakePublisher{}
	posts := &fakePosts{err: errors.New("insert failed")}
	ch := &fakeChannels{}
	w := NewWorker(WorkerDeps{Publisher: pub, Posts: posts, Channels: ch})

	raw := domain.RawPost{ChannelID: 100, TGMsgID: 9, Text: "Hello world"}
	data, _ := json.Marshal(raw)

	err := w.Handle(context.Background(), data)
	require.Error(t, err)
	require.Empty(t, pub.published)
}

func TestHandle_PublisherError(t *testing.T) {
	pub := &fakePublisher{err: errors.New("nats down")}
	posts := &fakePosts{}
	ch := &fakeChannels{}
	w := NewWorker(WorkerDeps{Publisher: pub, Posts: posts, Channels: ch})

	raw := domain.RawPost{ChannelID: 100, TGMsgID: 10, Text: "Hello world"}
	data, _ := json.Marshal(raw)

	err := w.Handle(context.Background(), data)
	require.Error(t, err)
	// Post is still persisted — the publish error is the only failure.
	require.Len(t, posts.saved, 1)
}

func TestPostsRepoFunc_AdaptsCallable(t *testing.T) {
	called := false
	var f PostsRepoFunc = func(_ context.Context, p NormalizedPostInsert) (int64, error) {
		called = true
		require.Equal(t, "x", p.TextClean)
		return 42, nil
	}
	id, err := f.Insert(context.Background(), NormalizedPostInsert{TextClean: "x"})
	require.NoError(t, err)
	require.Equal(t, int64(42), id)
	require.True(t, called)
}
