package normalize

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"

	"github.com/virsi/mute-bot/internal/domain"
	"github.com/virsi/mute-bot/internal/queue"
)

// Publisher is the slice of the queue.Publisher contract this worker needs.
// Kept narrow so unit tests can substitute a fake.
type Publisher interface {
	Publish(ctx context.Context, subject string, payload any) error
}

// PostsRepo is the slice of the posts repository contract this worker needs.
// Takes NormalizedPostInsert (not domain.NormalizedPost) so the storage
// adapter does not depend on the domain package.
type PostsRepo interface {
	Insert(ctx context.Context, p NormalizedPostInsert) (int64, error)
}

// ChannelsRepo is the slice of the channels repository contract this worker
// needs to translate a tg-side channel id into the internal DB id.
type ChannelsRepo interface {
	ResolveOrCreate(ctx context.Context, tgID int64) (int64, error)
}

// PostsRepoFunc adapts an ordinary function to the PostsRepo interface so
// the wiring layer (cmd/processor) can build the adapter inline without a
// separate type.
type PostsRepoFunc func(ctx context.Context, p NormalizedPostInsert) (int64, error)

// Insert calls f.
func (f PostsRepoFunc) Insert(ctx context.Context, p NormalizedPostInsert) (int64, error) {
	return f(ctx, p)
}

// NormalizedPostInsert is the stable insert input the worker hands to the
// posts repository. It carries only what the storage layer needs and avoids
// leaking domain.NormalizedPost into adapters.
type NormalizedPostInsert struct {
	ChannelID int64
	TGMsgID   int64
	TextRaw   string
	TextClean string
	TextHash  [32]byte
	Lang      string
	PostedAt  time.Time
}

// NormalizedPostEvent is the event published to ingest.normalized after a
// post has been cleaned and persisted. Downstream dedup consumes it.
type NormalizedPostEvent struct {
	PostID    int64     `json:"post_id"`
	ChannelID int64     `json:"channel_id"`
	TextClean string    `json:"text_clean"`
	TextHash  [32]byte  `json:"text_hash"`
	Lang      string    `json:"lang"`
	PostedAt  time.Time `json:"posted_at"`
}

// WorkerDeps groups the worker's collaborators.
type WorkerDeps struct {
	Publisher Publisher
	Posts     PostsRepo
	Channels  ChannelsRepo
}

// Worker consumes raw posts off the ingest.raw subject, cleans and persists
// them, then republishes a NormalizedPostEvent on ingest.normalized.
type Worker struct {
	d WorkerDeps
}

// NewWorker builds a Worker bound to d. Caller is responsible for wiring d.
func NewWorker(d WorkerDeps) *Worker { return &Worker{d: d} }

// Handle is the JetStream message callback. It is idempotent: posts are
// inserted via ON CONFLICT DO UPDATE in PostsRepo so a redelivery returns the
// same post id and publishes the same event.
func (w *Worker) Handle(ctx context.Context, data []byte) error {
	var raw domain.RawPost
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("unmarshal raw post: %w", err)
	}

	clean := Clean(raw.Text)
	if clean == "" {
		// Drop posts that become empty after cleaning — they carry no signal.
		return nil
	}
	lang := DetectLang(clean)

	chDBID, err := w.d.Channels.ResolveOrCreate(ctx, raw.ChannelID)
	if err != nil {
		return fmt.Errorf("resolve channel: %w", err)
	}

	hash := sha256.Sum256([]byte(clean))
	in := NormalizedPostInsert{
		ChannelID: chDBID,
		TGMsgID:   raw.TGMsgID,
		TextRaw:   raw.Text,
		TextClean: clean,
		TextHash:  hash,
		Lang:      lang,
		PostedAt:  raw.PostedAt,
	}
	id, err := w.d.Posts.Insert(ctx, in)
	if err != nil {
		return fmt.Errorf("insert post: %w", err)
	}

	evt := NormalizedPostEvent{
		PostID:    id,
		ChannelID: chDBID,
		TextClean: clean,
		TextHash:  hash,
		Lang:      lang,
		PostedAt:  raw.PostedAt,
	}
	if err := w.d.Publisher.Publish(ctx, queue.SubjectNormalized, evt); err != nil {
		return fmt.Errorf("publish normalized: %w", err)
	}
	return nil
}
