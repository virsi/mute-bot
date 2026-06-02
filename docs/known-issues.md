# Known Issues / Tech Debt (Phase 1 → Phase 2)

Findings carried over from the Phase 1 QA review. Non-blocking, but should be addressed before or during Phase 2.

## Lint debt (pre-existing)

`golangci-lint run ./...` reports 42 findings in code already in `main`. Breakdown:
- `revive` — 28 (missing exported doc comments, naming)
- `gosec` — 6 (file perms, weak crypto in non-crypto contexts)
- `gofumpt` — 3 (formatting)
- `errcheck` — 3 (unchecked error returns)
- `errorlint` — 1 (type-assert on `error`)
- `nilerr` — 1 (returning nil with non-nil err)

Areas: `internal/llm/*`, `internal/storage/postgres/channels_repo.go`, others.

**Action:** dedicated `chore(lint): clean preexisting findings` pass before Phase 2 starts, ideally split per package so reviews stay small.

## Test gaps

These packages currently rely on indirect coverage only:

- `internal/storage/postgres/deliveries_repo.go` — exercised via integration tests of `digest.Assembler`, no direct repo-level tests
- `internal/storage/postgres/settings_repo.go` — same
- `internal/storage/redis/client.go`, `embedding_cache.go` — exercised via dedup tests only
- `internal/bot/api.go` — exercised via build / `cmd/bot-api` wiring only
- `internal/obs/tracing.go`, `internal/obs/logger.go` — thin wrappers, low risk

**Action:** add direct integration tests for `deliveries_repo` and `settings_repo` in Phase 2 — they will gain new methods (subscription updates, tier transitions) and direct coverage will matter.

## Config loader: no env-var expansion

`internal/config.Load` does not expand `${VAR}` references inside YAML values. Secrets must either be hardcoded in `config.yaml` (gitignored) or set via the small env-var override table in `applyEnvOverrides`. This is fine for solo-mode but breaks the 12-factor pattern.

**Action:** Phase 2 — add YAML template expansion (envsubst-style) so any `${SOMETHING}` in `configs/config.yaml` resolves from the environment at load time. Keep the explicit `MUTE_*` overrides as a backstop.

## Tracing not wired

`internal/obs/tracing.go::SetupTracing` exists but is not called from any `cmd/*/main.go`. No `OTLPEndpoint` field exists in `internal/config.Config`.

**Action:** Phase 2 — add `otlp_endpoint` to config, call `SetupTracing` in each cmd at startup, defer the shutdown function.

## Two processes hold the Bot API token

Both `cmd/bot-api` (polling for updates) and `cmd/processor` (sending digests) construct a `bot.BotAPI` from the same token. The polling side must be the only consumer of `getUpdates`; if `processor` accidentally calls polling helpers, two clients will fight for updates.

Currently `processor` only uses the API for `Send`, which is safe. Document this in the dogfooding doc setup so future maintainers don't accidentally start polling from `processor`.

**Action:** Phase 2 — split the `BotAPI` struct into a send-only client (used by `processor`) and a full client (used by `bot-api`), making the boundary explicit at the type level.

## LLM judge unused

`internal/dedup/llm_merge.go::LLMJudge` is implemented and unit-tested, but never invoked. The plan defers it to a Phase-2 reconciliation job that drains a Redis list `dedup:borderline` populated by `Matcher` for cosine distances in 0.15–0.25.

**Action:** Phase 2 — add `Matcher` push to `dedup:borderline` for borderline candidates + a periodic `cmd/processor` job that drains the list and calls `LLMJudge.Decide`, merging clusters when the judge returns `{"same":true,"confidence":>=0.8}`.

## Scheduler DST drift

`gocron.WithLocation(time.UTC)` means per-user TZ is honored by converting HH:MM → UTC at registration. After a DST shift the local fire time drifts ±1 hour until the next reload tick (5 min default), so users see at most one off-time digest.

**Action:** Phase 2 — switch to per-user time-zoned cron jobs (gocron v2 supports per-job `WithLocation`) so DST is handled correctly.
