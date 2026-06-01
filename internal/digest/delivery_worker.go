package digest

import (
	"context"
	"encoding/json"
	"fmt"
)

// AssemblerIface is the slice of *Assembler the delivery worker needs.
// Declared here (rather than reusing *Assembler directly) so the worker
// can be unit-tested with a stub. The interface name lives in the digest
// package because the worker is the only caller and the name does not
// pollute the digest package surface.
type AssemblerIface interface {
	Assemble(ctx context.Context, req AssembleRequest) error
}

// DeliveryWorker consumes delivery.scheduled events emitted by the
// scheduler (and on-demand /digest in future revisions) and dispatches
// each one to the Assembler.
//
// The handler is intentionally a thin shell — payload schema validation
// and title selection live here; everything else lives in the assembler.
type DeliveryWorker struct {
	a AssemblerIface
}

// NewDeliveryWorker constructs a DeliveryWorker that drives a. The narrow
// AssemblerIface receiver means callers can supply either *Assembler
// directly or a test stub.
func NewDeliveryWorker(a AssemblerIface) *DeliveryWorker {
	return &DeliveryWorker{a: a}
}

// deliveryEvent is the JetStream payload published on delivery.scheduled.
// Field names mirror the scheduler's publish: see internal/scheduler/cron.go.
type deliveryEvent struct {
	UserID   int64  `json:"user_id"`
	TGUserID int64  `json:"tg_user_id"`
	Channel  string `json:"channel"`
}

// Handle is the JetStream message callback. It unmarshals the event,
// picks the user-facing title from the channel tag, and forwards to
// the assembler. Malformed JSON is a hard error so the broker NACKs
// and (eventually) parks the message in the DLQ.
func (w *DeliveryWorker) Handle(ctx context.Context, data []byte) error {
	var evt deliveryEvent
	if err := json.Unmarshal(data, &evt); err != nil {
		return fmt.Errorf("unmarshal delivery.scheduled: %w", err)
	}
	return w.a.Assemble(ctx, AssembleRequest{
		UserID:   evt.UserID,
		TGUserID: evt.TGUserID,
		Channel:  evt.Channel,
		Title:    titleForChannel(evt.Channel),
	})
}

// titleForChannel maps a delivery channel tag to the human-facing digest
// title. Unknown tags fall back to the daily title — better to ship a
// weakly-labelled digest than to drop the message.
func titleForChannel(channel string) string {
	switch channel {
	case "weekly":
		return "Недельная сводка"
	default:
		return "Утренняя сводка"
	}
}
