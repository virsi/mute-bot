# Phase 3 — Decisions Delta

Adjustments to the original spec (`2026-06-03-news-digest-bot-design.md`) for Phase 3 work. Phase 2 carry-over still authoritative for anything not overridden.

## Scope

P3 ships in two milestones; custom-channel sources are explicitly deferred.

### P3-M1 — Weekly digest (Pro only)

- New delivery channel `weekly_digest`. Existing daily on-demand and scheduled paths unchanged.
- **Cadence:** every Sunday at 18:00 in the user's timezone (per-user TZ via the existing `digest_schedule` JSON, but reused for the weekly slot).
- **Content:** top-10 clusters from the trailing 7 days by `score`. Anti-repeat shares the cluster history but a separate `delivered_at::date` projection — weekly is independent of daily.
- **Format:** identical Markdown shape as daily digest, with header "Главное за неделю" and date range.
- **Tier gate:** Pro only. Free users requesting `/weekly` get the same upgrade message as other Pro commands.
- **Trigger surface:** scheduled via cron + on-demand via new `/weekly` command.

### P3-M2 — YooKassa billing provider

- New `Provider` implementation alongside `StarsProvider`. Both register in the billing service.
- **Plan:** `pro_30d_rub`, 150 ₽ / 30 days, recurring via YooKassa "saved card" (autopayment with `payment_method_id`).
- `/buy` now offers two inline buttons: `Купить за 99 ⭐` and `Картой за 150 ₽`. User picks the channel.
- **Webhook:** YooKassa pushes to a new public HTTPS endpoint (`/yookassa/webhook`). bot-api process serves it on the same HTTP server as `/metrics` / `/healthz`, separate port slot.
- **Auth:** YooKassa-signed payload validation (HMAC via shop secret). Reject any request not matching the signature.
- **Idempotency:** identical to Stars — `subscriptions UNIQUE (provider, provider_ref)` with provider name `"yookassa"` and `provider_ref = payment_id`.
- **Refunds out of scope** for P3. Manual handling via YooKassa dashboard until P4.

### Deferred — Custom channel sources

- Original P3 plan: Pro users add their own TG channels to ingest.
- **Blocker:** without MTProto the scraper only supports public channels via `https://t.me/s/<username>`, which is fine technically but the per-user/per-channel multi-tenancy on top of `tg-scraper` raises 3 problems we don't want to solve mid-flight:
  1. Channel ownership: one user's "favourite" channel is shared infra — many users may want the same one, so it's still a global pull, not per-user.
  2. Spam/abuse: anyone can add any channel, including dead/spammy ones that pollute the cluster space for all users.
  3. Authority score: arbitrary user-added channels have no curated authority signal, so ranker quality degrades.
- **Decision:** ship weekly + YooKassa first. Revisit custom sources once we have MTProto (channels become private-capable) or a curated authority model.
- Pro pricing-stack stays attractive on Phase 2 features (custom topics + alerts + weekly) without custom channels.

## New / changed invariants

- **INV-7 (Single weekly fan-out):** the weekly job emits one `delivery.scheduled` per Pro user per Sunday, even on retry or scheduler-restart. The same `gocron + advisory lock` mechanism that protects daily applies; weekly slot keyed `weekly:{user_id}:{YYYY-WW}`.
- **INV-8 (Billing provider isolation):** Stars and YooKassa share `subscriptions` but their `provider` strings never overlap; UNIQUE(provider, provider_ref) keeps webhook idempotency independent per channel. A grant from one provider does NOT cancel a grant from the other — periods stack (same `GrantPro` math as Stars-on-Stars).

## Not included in P3

- Refunds / cancellation flow
- A/B price tests
- Annual plans / multi-tier (Pro/Plus/etc.)
- Webhook retries / dead-letter handling (rely on YooKassa retries)
- Localisation beyond ru-RU
- Multi-currency display
- Custom channel sources (see Deferred above)
