-- 0002_user_tier.up.sql — enforce free/pro at the schema level + tier indexes.
-- Phase-1 schema (0001) already declared `tier text NOT NULL DEFAULT 'free'`
-- and a nullable `tier_until timestamptz`. This migration:
--   1. constrains tier to the two values supported by the billing service,
--   2. adds a btree index on tier for the bot's IsPro hot path,
--   3. adds a partial index on tier_until for the hourly expiry sweeper.

ALTER TABLE users
    DROP CONSTRAINT IF EXISTS users_tier_check;
ALTER TABLE users
    ADD CONSTRAINT users_tier_check CHECK (tier IN ('free','pro'));

CREATE INDEX IF NOT EXISTS users_tier_idx ON users(tier);
CREATE INDEX IF NOT EXISTS users_tier_until_idx ON users(tier_until) WHERE tier = 'pro';
