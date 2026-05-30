package mtproto

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/updates"
	"github.com/gotd/td/tg"

	"github.com/virsi/mute-bot/internal/domain"
	"github.com/virsi/mute-bot/internal/queue"
)

// Publisher is the surface used by Reader to emit raw posts onto the queue.
// It is intentionally narrow so tests can stub it without pulling in NATS.
type Publisher interface {
	Publish(ctx context.Context, subject string, payload any) error
}

// Reader subscribes to MTProto channel-message updates, extracts a
// domain.RawPost from each, and publishes it onto queue.SubjectRaw.
type Reader struct {
	client *telegram.Client
	pub    Publisher
	subj   string
}

// NewReader wires the gotd client to a Publisher.
func NewReader(c *telegram.Client, pub Publisher) *Reader {
	return &Reader{client: c, pub: pub, subj: queue.SubjectRaw}
}

// ExtractRawPost maps a gotd MessageClass to a domain.RawPost. Returns
// (zero, false) for service / empty messages or peers we do not consume
// (e.g. users). Pure-Go — safe to call in unit tests without any network.
func ExtractRawPost(msg tg.MessageClass) (domain.RawPost, bool) {
	m, ok := msg.(*tg.Message)
	if !ok || m == nil || m.Message == "" {
		return domain.RawPost{}, false
	}
	var chID int64
	switch p := m.PeerID.(type) {
	case *tg.PeerChannel:
		chID = p.ChannelID
	case *tg.PeerChat:
		chID = p.ChatID
	default:
		return domain.RawPost{}, false
	}
	return domain.RawPost{
		ChannelID: chID,
		TGMsgID:   int64(m.ID),
		Text:      m.Message,
		PostedAt:  time.Unix(int64(m.Date), 0).UTC(),
	}, true
}

// Run drives the gotd client until ctx is cancelled. Authentication must
// already have happened (see Authenticate). The function blocks while the
// updates manager processes the stream.
func (r *Reader) Run(ctx context.Context) error {
	dispatcher := tg.NewUpdateDispatcher()
	dispatcher.OnNewChannelMessage(func(ctx context.Context, _ tg.Entities, u *tg.UpdateNewChannelMessage) error {
		rp, ok := ExtractRawPost(u.Message)
		if !ok {
			return nil
		}
		if err := r.pub.Publish(ctx, r.subj, rp); err != nil {
			slog.Error("mtproto: publish raw post",
				slog.Any("err", err),
				slog.Int64("channel_id", rp.ChannelID),
				slog.Int64("msg_id", rp.TGMsgID),
			)
		}
		return nil
	})

	mgr := updates.New(updates.Config{Handler: dispatcher})

	return r.client.Run(ctx, func(ctx context.Context) error {
		status, err := r.client.Auth().Status(ctx)
		if err != nil {
			return fmt.Errorf("auth status: %w", err)
		}
		if !status.Authorized {
			return fmt.Errorf("session not authorized; run Authenticate before Run")
		}
		return mgr.Run(ctx, r.client.API(), status.User.GetID(), updates.AuthOptions{
			IsBot: false,
			OnStart: func(_ context.Context) {
				slog.Info("mtproto: updates manager started",
					slog.Int64("user_id", status.User.GetID()),
				)
			},
		})
	})
}
