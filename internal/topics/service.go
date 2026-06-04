package topics

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/pgvector/pgvector-go"

	"github.com/virsi/mute-bot/internal/llm"
)

// MaxTopicsPerUser caps how many custom topics a single Pro user can
// own. Enforced by AddTopic so a runaway /topics add loop cannot
// exhaust the LLM budget or pollute the centroid match.
const MaxTopicsPerUser = 20

// DefaultMatchDistance is the cosine distance threshold under which a
// cluster centroid is considered to match a user's topic embedding. A
// distance of 0.30 corresponds to cosine similarity ≈ 0.70 — the
// figure recommended by the plan.
const DefaultMatchDistance = float32(0.30)

// ErrTooManyTopics is returned by AddTopic when the per-user cap has
// already been hit. Bot handlers translate this into a friendly RU
// message; the error is sentinel so callers can branch on errors.Is.
var ErrTooManyTopics = errors.New("topic limit reached")

// ErrEmptyName is returned by AddTopic when the trimmed name is empty.
// Lets the bot reject "/topics add   " without persisting noise.
var ErrEmptyName = errors.New("topic name must not be empty")

// ErrEmptyEmbedding signals that the embedder returned zero vectors —
// usually a transient upstream problem. Treated as retryable by the
// caller; not wrapped as ErrTooManyTopics to avoid misleading users.
var ErrEmptyEmbedding = errors.New("embedder returned empty vector")

// Service orchestrates custom-topic operations for Pro users.
type Service struct {
	repo          Repo
	emb           Embedder
	model         string
	maxDistance   float32
	maxTopicCount int
}

// Deps configures NewService. Model defaults to text-embedding-3-small
// when empty (matches the dedup pipeline). MaxDistance defaults to
// DefaultMatchDistance. MaxTopics defaults to MaxTopicsPerUser.
type Deps struct {
	Repo        Repo
	Embedder    Embedder
	Model       string
	MaxDistance float32
	MaxTopics   int
}

// NewService constructs a Service. Repo and Embedder are required.
func NewService(d Deps) *Service {
	if d.Model == "" {
		d.Model = "text-embedding-3-small"
	}
	if d.MaxDistance <= 0 {
		d.MaxDistance = DefaultMatchDistance
	}
	if d.MaxTopics <= 0 {
		d.MaxTopics = MaxTopicsPerUser
	}
	return &Service{
		repo:          d.Repo,
		emb:           d.Embedder,
		model:         d.Model,
		maxDistance:   d.MaxDistance,
		maxTopicCount: d.MaxTopics,
	}
}

// AddTopic embeds name once and persists it for userID. The caller must
// have verified userID is Pro upstream (bot.RequirePro middleware) —
// the service trusts the gate so it can be unit-tested in isolation.
//
// Returns ErrTooManyTopics when the per-user cap is reached.
// Returns ErrEmptyName when name is whitespace.
// Returns ErrEmptyEmbedding when the embedder returns zero vectors.
// Returns a wrapped error from the LLM or repo for other failures.
func (s *Service) AddTopic(ctx context.Context, userID int64, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return ErrEmptyName
	}
	n, err := s.repo.Count(ctx, userID)
	if err != nil {
		return fmt.Errorf("count: %w", err)
	}
	if n >= s.maxTopicCount {
		return ErrTooManyTopics
	}
	resp, err := s.emb.Embed(ctx, llm.EmbedRequest{Model: s.model, Texts: []string{name}})
	if err != nil {
		return fmt.Errorf("embed: %w", err)
	}
	if len(resp.Vectors) == 0 || len(resp.Vectors[0]) == 0 {
		return ErrEmptyEmbedding
	}
	if _, err := s.repo.Insert(ctx, userID, name, pgvector.NewVector(resp.Vectors[0])); err != nil {
		return fmt.Errorf("persist: %w", err)
	}
	return nil
}

// RemoveTopic deletes the named topic for userID. Idempotent — removing
// a missing row is silently treated as success at the repo layer.
func (s *Service) RemoveTopic(ctx context.Context, userID int64, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return ErrEmptyName
	}
	if err := s.repo.Delete(ctx, userID, name); err != nil {
		return fmt.Errorf("delete: %w", err)
	}
	return nil
}

// ListTopics returns just the human-facing names, oldest first.
func (s *Service) ListTopics(ctx context.Context, userID int64) ([]string, error) {
	rows, err := s.repo.ListByUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list: %w", err)
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Name)
	}
	return out, nil
}

// MatchesAny reports whether the digest assembler should keep a
// cluster for userID. The contract is:
//
//   - When the user owns zero custom topics, returns (true, nil) so
//     the assembler treats it as "no filter active" and keeps every
//     cluster (default behavior — Free users with no topics see all).
//   - Otherwise, returns true iff at least one stored topic embedding
//     lies within the configured cosine distance of centroid.
//
// The "no topics → true" branch deliberately differs from the bare
// repo MatchesAny so the assembler does not need an extra Count call.
func (s *Service) MatchesAny(ctx context.Context, userID int64, centroid pgvector.Vector) (bool, error) {
	n, err := s.repo.Count(ctx, userID)
	if err != nil {
		return false, fmt.Errorf("count: %w", err)
	}
	if n == 0 {
		return true, nil
	}
	ok, err := s.repo.MatchesAny(ctx, userID, centroid, s.maxDistance)
	if err != nil {
		return false, fmt.Errorf("matches any: %w", err)
	}
	return ok, nil
}
