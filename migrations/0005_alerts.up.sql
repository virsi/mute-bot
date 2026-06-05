-- 0005_alerts.up.sql — real-time alerts throttling. alerts_enabled and
-- alert_threshold are already part of user_settings from 0001; this
-- migration only adds the per-user throttle window (minutes between two
-- alerts on the same cluster topic). Default = 30 min matches the Pro
-- onboarding copy.
ALTER TABLE user_settings
    ADD COLUMN IF NOT EXISTS alert_throttle_minutes int NOT NULL DEFAULT 30;
