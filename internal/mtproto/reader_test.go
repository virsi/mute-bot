package mtproto

import (
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/stretchr/testify/require"
)

func TestExtractRawPost_FromChannelMessage(t *testing.T) {
	t.Parallel()
	now := time.Now().Unix()
	m := &tg.Message{
		ID:      42,
		Message: "Hello world",
		Date:    int(now),
		PeerID:  &tg.PeerChannel{ChannelID: 12345},
	}
	rp, ok := ExtractRawPost(m)
	require.True(t, ok)
	require.Equal(t, int64(12345), rp.ChannelID)
	require.Equal(t, int64(42), rp.TGMsgID)
	require.Equal(t, "Hello world", rp.Text)
	require.Equal(t, time.Unix(now, 0).UTC(), rp.PostedAt)
}

func TestExtractRawPost_FromChatMessage(t *testing.T) {
	t.Parallel()
	m := &tg.Message{
		ID:      7,
		Message: "Chat message",
		Date:    int(time.Now().Unix()),
		PeerID:  &tg.PeerChat{ChatID: 999},
	}
	rp, ok := ExtractRawPost(m)
	require.True(t, ok)
	require.Equal(t, int64(999), rp.ChannelID)
	require.Equal(t, int64(7), rp.TGMsgID)
}

func TestExtractRawPost_SkipsServiceMessages(t *testing.T) {
	t.Parallel()
	_, ok := ExtractRawPost(&tg.MessageService{ID: 1})
	require.False(t, ok)
}

func TestExtractRawPost_SkipsEmptyMessage(t *testing.T) {
	t.Parallel()
	_, ok := ExtractRawPost(&tg.MessageEmpty{ID: 1})
	require.False(t, ok)
}

func TestExtractRawPost_SkipsMessagesWithoutText(t *testing.T) {
	t.Parallel()
	m := &tg.Message{
		ID:      10,
		Message: "",
		PeerID:  &tg.PeerChannel{ChannelID: 1},
	}
	_, ok := ExtractRawPost(m)
	require.False(t, ok)
}

func TestExtractRawPost_SkipsUnknownPeer(t *testing.T) {
	t.Parallel()
	m := &tg.Message{
		ID:      11,
		Message: "Hello",
		PeerID:  &tg.PeerUser{UserID: 42},
	}
	_, ok := ExtractRawPost(m)
	require.False(t, ok)
}
