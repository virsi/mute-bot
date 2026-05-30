package mtproto

import (
	"context"
	"fmt"

	"github.com/gotd/td/tg"

	"github.com/virsi/mute-bot/internal/queue"
)

// StateRepo is the persistence surface needed by Catchup. It is satisfied by
// postgres.SessionStateRepo. Defined here so the package does not have to
// import storage/postgres (which would also leak pg into mtproto's deps).
type StateRepo interface {
	GetLastMsgID(ctx context.Context, channelID int64) (int64, error)
	UpsertLastMsgID(ctx context.Context, channelID, lastMsgID int64) error
}

// Catchup backfills missed channel messages on session-reader restart by
// calling messages.getHistory with min_id = last_seen_msg_id and publishing
// each new message onto the raw-ingest subject.
type Catchup struct {
	api   *tg.Client
	state StateRepo
	pub   Publisher
	limit int
}

// NewCatchup wires Catchup with sensible defaults (limit=100).
func NewCatchup(api *tg.Client, state StateRepo, pub Publisher) *Catchup {
	return &Catchup{api: api, state: state, pub: pub, limit: 100}
}

// Backfill fetches messages newer than the stored cursor for localChannelDBID
// and publishes them. The cursor is then advanced to the maximum tg msg id
// seen in this batch. localChannelDBID is the row id in the postgres
// "channels" table; ch holds the Telegram-side identifiers.
func (c *Catchup) Backfill(ctx context.Context, ch ChannelRef, localChannelDBID int64) error {
	last, err := c.state.GetLastMsgID(ctx, localChannelDBID)
	if err != nil {
		return fmt.Errorf("get last msg id: %w", err)
	}

	hist, err := c.api.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
		Peer:     &tg.InputPeerChannel{ChannelID: ch.ChannelID, AccessHash: ch.AccessHash},
		OffsetID: 0,
		Limit:    c.limit,
		MinID:    int(last),
	})
	if err != nil {
		return fmt.Errorf("get history: %w", err)
	}

	mod, ok := hist.AsModified()
	if !ok {
		return nil
	}

	var maxID int64
	for _, m := range mod.GetMessages() {
		rp, ok := ExtractRawPost(m)
		if !ok {
			continue
		}
		if rp.TGMsgID <= last {
			continue
		}
		// Overwrite ChannelID with the Telegram-side id from ChannelRef so
		// downstream consumers see the same value the live update stream
		// produces.
		rp.ChannelID = ch.ChannelID
		if err := c.pub.Publish(ctx, queue.SubjectRaw, rp); err != nil {
			return fmt.Errorf("publish backfill: %w", err)
		}
		if rp.TGMsgID > maxID {
			maxID = rp.TGMsgID
		}
	}

	if maxID > 0 {
		if err := c.state.UpsertLastMsgID(ctx, localChannelDBID, maxID); err != nil {
			return fmt.Errorf("upsert cursor: %w", err)
		}
	}
	return nil
}
