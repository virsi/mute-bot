-- 0001_initial.up.sql — Phase 1 schema.
-- Channels we read from, posts ingested, clusters of related posts,
-- per-post embeddings, users + their settings + delivery history,
-- and per-channel MTProto cursor state.

CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE channels (
  id              bigserial PRIMARY KEY,
  tg_channel_id   bigint UNIQUE NOT NULL,
  username        text,
  title           text,
  authority_score int NOT NULL DEFAULT 50,
  added_by        bigint,
  active          bool NOT NULL DEFAULT true,
  created_at      timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE clusters (
  id              bigserial PRIMARY KEY,
  headline        text NOT NULL DEFAULT '',
  summary         text NOT NULL DEFAULT '',
  topics          text[] NOT NULL DEFAULT '{}',
  severity        int NOT NULL DEFAULT 0,
  coverage        int NOT NULL DEFAULT 1,
  score           real NOT NULL DEFAULT 0,
  first_seen_at   timestamptz NOT NULL DEFAULT now(),
  last_updated_at timestamptz NOT NULL DEFAULT now(),
  status          text NOT NULL DEFAULT 'active'
);
CREATE INDEX clusters_last_updated_score_idx ON clusters (last_updated_at DESC, score DESC);
CREATE INDEX clusters_topics_gin_idx        ON clusters USING gin(topics);

CREATE TABLE posts (
  id           bigserial PRIMARY KEY,
  channel_id   bigint NOT NULL REFERENCES channels(id),
  tg_msg_id    bigint NOT NULL,
  text_raw     text NOT NULL,
  text_clean   text NOT NULL,
  text_hash    bytea NOT NULL,
  lang         text,
  posted_at    timestamptz NOT NULL,
  ingested_at  timestamptz NOT NULL DEFAULT now(),
  cluster_id   bigint REFERENCES clusters(id),
  UNIQUE(channel_id, tg_msg_id)
);
CREATE INDEX posts_posted_at_idx ON posts(posted_at);
CREATE INDEX posts_cluster_id_idx ON posts(cluster_id);
CREATE INDEX posts_text_hash_idx ON posts(text_hash);

CREATE TABLE post_embeddings (
  post_id    bigint PRIMARY KEY REFERENCES posts(id) ON DELETE CASCADE,
  embedding  vector(1536) NOT NULL,
  model      text NOT NULL
);
CREATE INDEX post_embeddings_vec_idx ON post_embeddings
  USING ivfflat (embedding vector_cosine_ops) WITH (lists = 100);

CREATE TABLE users (
  id           bigserial PRIMARY KEY,
  tg_user_id   bigint UNIQUE NOT NULL,
  tg_username  text,
  tier         text NOT NULL DEFAULT 'free',
  tier_until   timestamptz,
  trial_used   bool NOT NULL DEFAULT false,
  lang         text NOT NULL DEFAULT 'ru',
  blocked      bool NOT NULL DEFAULT false,
  created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE user_settings (
  user_id          bigint PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  topics           text[] NOT NULL DEFAULT '{}',
  threshold        int NOT NULL DEFAULT 50,
  digest_schedule  jsonb NOT NULL DEFAULT '{"times":["08:00","19:00"],"tz":"Europe/Moscow"}',
  alerts_enabled   bool NOT NULL DEFAULT false,
  alert_threshold  int NOT NULL DEFAULT 85,
  weekly_enabled   bool NOT NULL DEFAULT false,
  updated_at       timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE deliveries (
  id           bigserial PRIMARY KEY,
  user_id      bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  cluster_id   bigint NOT NULL REFERENCES clusters(id) ON DELETE CASCADE,
  delivered_at timestamptz NOT NULL DEFAULT now(),
  channel      text NOT NULL,
  UNIQUE(user_id, cluster_id)
);
CREATE INDEX deliveries_user_time_idx ON deliveries(user_id, delivered_at DESC);

CREATE TABLE session_state (
  channel_id      bigint PRIMARY KEY REFERENCES channels(id) ON DELETE CASCADE,
  last_msg_id     bigint NOT NULL DEFAULT 0,
  last_updated_at timestamptz NOT NULL DEFAULT now()
);
