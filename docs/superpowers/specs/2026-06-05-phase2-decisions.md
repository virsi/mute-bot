# Phase 2 — Decisions Delta to Original Spec

Adjustments to `2026-06-03-news-digest-bot-design.md` taken on 2026-06-05 before Phase 2 implementation starts. Original spec stays authoritative for everything not overridden here.

## Pricing

- **Pro = 99 Stars / month** (≈150₽). Calibrated low for early-adopter conversion; revisit after 50 paid users.
- Phase 2 ships **TG Stars only**. ЮКасса deferred to Phase 3 as originally planned.
- One tier transition: `free → pro` for one month, auto-renew via TG `subscription_period`.

## Trial — REMOVED

- **FR-8.1 trial is dropped.** New users start `free` immediately. `/buy` is the only path to `pro`.
- Rationale: trial burns LLM tokens on Free conversion attempts that may not convert (Inv-1..Inv-4 favored). Smaller engineering surface for Phase 2. Trial can return later if conversion data justifies.
- `users.trial_used` column stays (forward-compat) but is never set in Phase 2 code.

## Tech-debt purge ordering

P2-M1 closes everything in `docs/known-issues.md` before any P2 feature merges:
1. Lint cleanup (42 findings)
2. Direct integration tests for `deliveries_repo`, `settings_repo`
3. YAML `${VAR}` expansion in `internal/config`
4. OTLP tracing wired into all 4 `cmd/*/main.go`
5. Split `BotAPI` into `sender` (processor) + `client` (bot-api)
6. Wire `LLMJudge` into borderline reconciliation job
7. Per-job `WithLocation` in scheduler (DST fix)

## Ingest source

- **Phase 2 sticks with `cmd/tg-scraper`** (HTML). MTProto deferred until `api_id` becomes available.
- Real-time alerts (P2-M5) will run at scraper polling cadence (60s) — acceptable for breaking-news threshold.
- If MTProto arrives mid-P2, both processes can publish to `ingest.raw` simultaneously; `posts UNIQUE (channel_id, tg_msg_id)` handles deduplication. Add MTProto channel-id translation later.

## Scope explicitly OUT of Phase 2

- Custom channel sources (P3)
- Weekly digest (P3)
- Web cabinet (no plans)
- ЮКасса (P3)
- Per-user channel authority overrides
- A/B testing infra for pricing

## New invariants

- **INV-5 (Free user latency budget):** Free user's digest assembly must not invoke per-user LLM calls. Custom topics and alerts are Pro-only because they require per-user LLM work (topic embedding match + severity-based push). Free uses preset-topic shared classification only.
- **INV-6 (Subscription idempotency):** `successful_payment` webhook may fire twice for the same `provider_ref`. `subscriptions UNIQUE (provider, provider_ref)` rejects duplicates; activation is read-after-insert.
