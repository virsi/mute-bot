DROP INDEX IF EXISTS subscriptions_renew_idx;
ALTER TABLE subscriptions DROP COLUMN IF EXISTS payment_method_id;
