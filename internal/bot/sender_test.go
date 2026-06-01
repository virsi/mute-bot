package bot

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type fakeAPI struct {
	mu   sync.Mutex
	sent []sentMsg
}

type sentMsg struct {
	chatID int64
	text   string
	at     time.Time
}

func (f *fakeAPI) Send(_ context.Context, chatID int64, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, sentMsg{chatID: chatID, text: text, at: time.Now()})
	return nil
}

func (f *fakeAPI) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

func TestSender_RateLimitsPerChat(t *testing.T) {
	api := &fakeAPI{}
	s := NewSender(SenderDeps{API: api, PerChatPerSec: 2})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	const n = 4
	start := time.Now()
	for range n {
		require.NoError(t, s.SendDigest(ctx, 555, "msg"))
	}
	elapsed := time.Since(start)

	// 2 tokens are in the bucket initially -> first 2 are immediate. Then we
	// must wait for ~1s (2 refills at 2/sec). Allow some slack to keep the
	// test robust under load. The lower bound is the load-bearing assertion.
	require.GreaterOrEqual(t, elapsed, 900*time.Millisecond,
		"4 messages at 2/sec must take ~1s, got %v", elapsed)
	require.Less(t, elapsed, 3*time.Second, "should not be slower than ~1.5s, got %v", elapsed)
	require.Equal(t, n, api.count())
}

func TestSender_PerChatIndependent(t *testing.T) {
	api := &fakeAPI{}
	s := NewSender(SenderDeps{API: api, PerChatPerSec: 1})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// One message each to two different chats — both should pass immediately
	// from each chat's initial token.
	start := time.Now()
	require.NoError(t, s.SendDigest(ctx, 1, "a"))
	require.NoError(t, s.SendDigest(ctx, 2, "b"))
	elapsed := time.Since(start)

	require.Less(t, elapsed, 200*time.Millisecond,
		"separate chats must not block each other, got %v", elapsed)
	require.Equal(t, 2, api.count())
}

func TestSender_DefaultPerChatPerSec(t *testing.T) {
	api := &fakeAPI{}
	s := NewSender(SenderDeps{API: api}) // no PerChatPerSec set
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// Default is 1/sec — one send must succeed using the initial token.
	require.NoError(t, s.SendDigest(ctx, 1, "hello"))
	require.Equal(t, 1, api.count())
}

func TestSender_RespectsContextCancellation(t *testing.T) {
	api := &fakeAPI{}
	s := NewSender(SenderDeps{API: api, PerChatPerSec: 1})

	// Burn the initial token.
	require.NoError(t, s.SendDigest(context.Background(), 7, "first"))

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	// Bucket is empty; the next send must wait ~1s but ctx cancels at 50ms.
	err := s.SendDigest(ctx, 7, "second")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Equal(t, 1, api.count(), "second message must not have been sent")
}
