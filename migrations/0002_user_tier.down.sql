-- 0002_user_tier.down.sql — reverse of 0002. Keeps the column data
-- intact, only drops the check + indexes.

DROP INDEX IF EXISTS users_tier_until_idx;
DROP INDEX IF EXISTS users_tier_idx;
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_tier_check;
