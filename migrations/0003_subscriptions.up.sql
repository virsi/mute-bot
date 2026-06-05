-- 0003_subscriptions.up.sql — TG Stars billing rows.
-- The (provider, provider_ref) UNIQUE constraint is the idempotency key:
-- a retried successful_payment webhook with the same charge id will be
-- swallowed by ON CONFLICT DO NOTHING, ensuring exactly one Pro grant per
-- payment.

CREATE TABLE subscriptions (
    id           bigserial PRIMARY KEY,
    user_id      bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    provider     text NOT NULL,
    provider_ref text NOT NULL,
    plan         text NOT NULL,
    started_at   timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NULL,
    UNIQUE(provider, provider_ref)
);

CREATE INDEX subscriptions_user_id_idx ON subscriptions(user_id);
