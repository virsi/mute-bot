-- 0006_weekly_deliveries.up.sql — anti-repeat for weekly digests.
--
-- One row per (user_id, cluster_id) in a single weekly digest. A second row
-- per (user_id, iso_week) is forbidden — drives INV-7 (Single weekly fan-out).
-- iso_week is a TEXT in "YYYY-WW" format computed by the application so it
-- survives DST and YYYY-W53 edges deterministically.
CREATE TABLE weekly_deliveries (
    id           bigserial PRIMARY KEY,
    user_id      bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    cluster_id   bigint NOT NULL REFERENCES clusters(id) ON DELETE CASCADE,
    iso_week     text NOT NULL,
    delivered_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(user_id, cluster_id, iso_week)
);
CREATE INDEX weekly_deliveries_user_isoweek_idx
    ON weekly_deliveries(user_id, iso_week)
    WHERE iso_week <> '';
CREATE INDEX weekly_deliveries_user_time_idx
    ON weekly_deliveries(user_id, delivered_at DESC);
