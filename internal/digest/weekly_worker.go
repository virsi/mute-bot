package digest

import (
	"context"
	"encoding/json"
	"fmt"
)

// WeeklyAssemblerIface is the slice of *WeeklyAssembler the worker calls.
// Defined as an interface so cmd wiring and tests can substitute alternative
// implementations without importing the full assembler struct.
type WeeklyAssemblerIface interface {
	BuildWeekly(ctx context.Context, req WeeklyRequest) error
}

// WeeklyWorker is the JetStream subscriber bound to
// queue.SubjectDeliveryWeeklySched. It is intentionally a thin shell: the
// payload is unmarshalled here, the actual work lives in WeeklyAssembler.
type WeeklyWorker struct{ a WeeklyAssemblerIface }

// NewWeeklyWorker constructs a WeeklyWorker bound to a.
func NewWeeklyWorker(a WeeklyAssemblerIface) *WeeklyWorker { return &WeeklyWorker{a: a} }

// weeklyEvent is the payload shape the scheduler publishes. Field names
// mirror WeeklyRequest one-to-one so we can convert with a plain type
// conversion instead of a manual struct literal.
type weeklyEvent struct {
	UserID   int64 `json:"user_id"`
	TGUserID int64 `json:"tg_user_id"`
}

// Handle parses the event and forwards to the assembler. Malformed JSON is
// a hard error so JetStream NACKs and (eventually) parks the message in DLQ.
func (w *WeeklyWorker) Handle(ctx context.Context, data []byte) error {
	var evt weeklyEvent
	if err := json.Unmarshal(data, &evt); err != nil {
		return fmt.Errorf("unmarshal delivery.weekly_scheduled: %w", err)
	}
	return w.a.BuildWeekly(ctx, WeeklyRequest(evt))
}
