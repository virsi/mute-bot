-- 0004_custom_topics.up.sql — Pro users describe their own topics by name;
-- a vector(1536) embedding lets us match them against cluster centroids
-- without touching the shared classifier prompt (preserves INV-3). The
-- embedding column shares dimensionality with post_embeddings so the same
-- text-embedding-3-small model drives both sides of the match.

CREATE TABLE user_topics (
    id          bigserial PRIMARY KEY,
    user_id     bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name        text NOT NULL,
    embedding   vector(1536) NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE(user_id, name)
);

CREATE INDEX user_topics_user_id_idx ON user_topics(user_id);
CREATE INDEX user_topics_embedding_idx ON user_topics
    USING ivfflat (embedding vector_cosine_ops) WITH (lists = 100);
