-- 0007_subscriptions_payment_method.up.sql — track saved-card refs for
-- YooKassa autopayment. Stars rows leave this column NULL.
ALTER TABLE subscriptions
    ADD COLUMN IF NOT EXISTS payment_method_id text;

-- Partial index drives the renewer's ListExpiring scan: only YooKassa rows
-- (or any future provider with saved-card support) carry a non-NULL
-- payment_method_id, so the index stays small and the scan is cheap.
CREATE INDEX IF NOT EXISTS subscriptions_renew_idx
    ON subscriptions(expires_at)
    WHERE payment_method_id IS NOT NULL;
