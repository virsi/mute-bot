// Package topics is the Pro-only custom-topic subsystem. It embeds a
// user-supplied topic name once on /topics add, persists the embedding,
// and answers MatchesAny calls by pushing a cosine comparison into the
// pgvector index. The package owns no transport — bot.Handlers calls
// AddTopic/RemoveTopic/ListTopics; digest.Assembler calls MatchesAny.
//
// Invariants:
//   - Embedding cost is paid exactly once per topic at AddTopic time.
//     The digest hot path never invokes the LLM (INV-5).
//   - The 20-topic per-user cap is enforced before the embed call so a
//     spammy /topics add cannot drain budget.
//   - MatchesAny returns false when the user has no custom topics; the
//     assembler interprets that as "filter inactive" so Free users (who
//     cannot add topics in the first place) see every cluster.
package topics

import (
	"context"

	"github.com/pgvector/pgvector-go"

	"github.com/virsi/mute-bot/internal/llm"
	"github.com/virsi/mute-bot/internal/storage/postgres"
)

// Repo is the slice of postgres.UserTopicsRepo the service depends on.
// Kept narrow so unit tests can stand up a fake without spinning a
// Postgres container.
type Repo interface {
	Insert(ctx context.Context, userID int64, name string, emb pgvector.Vector) (int64, error)
	ListByUser(ctx context.Context, userID int64) ([]postgres.UserTopic, error)
	Count(ctx context.Context, userID int64) (int, error)
	Delete(ctx context.Context, userID int64, name string) error
	MatchesAny(ctx context.Context, userID int64, v pgvector.Vector, maxDistance float32) (bool, error)
}

// Embedder is the slice of llm.Provider the service depends on. Only
// called inside AddTopic — never on a per-digest cluster filter (INV-5).
type Embedder interface {
	Embed(ctx context.Context, req llm.EmbedRequest) (llm.EmbedResponse, error)
}
