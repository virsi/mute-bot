# Phase 1 Dogfooding Checklist

Operational runbook for the single-user Solo-mode MVP. Use as a sanity gate before declaring Phase 1 done and as a tuning log during the first two weeks of real-world use.

## Setup

- [ ] `my.telegram.org/apps` registered. `api_id` + `api_hash` placed in `.env` (or `configs/config.yaml`)
- [ ] Secondary SIM-bound TG account prepared for `session-reader` (do **not** use personal account — risk of bans on aggressive read-throughput)
- [ ] At least 10 RU news channels populated in `configs/channels.yaml` (start broad, prune later)
- [ ] `OPENAI_API_KEY` in environment, monthly budget set in `config.yaml`
- [ ] `BOT_TOKEN` from @BotFather in environment
- [ ] `make docker-up && make migrate-up` — infra healthy, schema migrated
- [ ] All four processes start without errors:
  - [ ] `session-reader` connects to TG via MTProto
  - [ ] `processor` subscribes to all five JetStream consumers
  - [ ] `bot-api` registers `/start /digest /settings /threshold /topics`
  - [ ] `scheduler` loads `users JOIN user_settings.digest_schedule` and ticks
- [ ] `/metrics` reachable on ports `:9101` (session-reader), `:9102` (processor), `:9103` (bot-api), `:9104` (scheduler)
- [ ] `/healthz` returns 200 on all four ports
- [ ] First digest received within 30 min of running

## Two-week observation log

Track daily for two weeks (2026-06-03 → 2026-06-17). Capture in a personal notes file or spreadsheet:

- [ ] Posts received per day (Prometheus `ingest_posts_total` sum)
- [ ] Clusters formed per day (`SELECT count(*) FROM clusters WHERE first_seen_at::date = current_date`)
- [ ] Dedup match kind distribution (`dedup_match_kind_total{kind=minhash|embedding|new}`)
- [ ] LLM cost so far (`llm_cost_usd_total`) + monthly budget remaining (`llm_budget_used_ratio`)
- [ ] Digests delivered (`digest_sent_total`)
- [ ] Subjective notes:
  - Missed important events?
  - Bad clustering (two events fused, or one event split)?
  - Spammy digest (too many low-importance items)?
  - Wrong topic assignments?
  - Headlines/summaries inaccurate?

## Tuning loop (after 7 days)

- [ ] Review `dedup_match_kind_total` ratios:
  - Too many `new` matches → embedding cosine threshold too strict; lower `MaxDistance` in `internal/dedup/cluster_matcher.go`
  - Too many false merges → raise embedding threshold or wire LLM judge from `internal/dedup/llm_merge.go` into M10 batch reconciliation
- [ ] Tune ranker weights (`internal/rank/ranker.go` `Weights{}`) by hand based on which clusters survive the digest vs which important ones get filtered. Defaults: `w_cov=0.4 w_auth=0.3 w_sev=0.3`.
- [ ] Update `internal/classify/prompts/classifier.v1.txt` if topic assignments are systematically wrong. Bump filename to `classifier.v2.txt` when changes are non-trivial.
- [ ] Re-curate `channels.authority_score` if a "trusted" outlet is consistently spreading misinformation, or if a niche channel is consistently first to a story.
- [ ] Check `llm_budget_used_ratio` — if approaching 80% mid-month, lower classifier temperature or reduce post sample size in `Classifier.joinPosts`.

## Corpus harvest

- [ ] After 7 days, sample 200–500 representative posts from `posts` table into `tests/fixtures/posts_corpus_v1.jsonl`
- [ ] Hand-label cluster groupings (`cluster_label`: free-form string shared by all posts about same event)
- [ ] Hand-label importance on a 1–5 scale (`severity_label`)
- [ ] Run `EVAL=1 go test -tags=integration ./tests/eval/...` — confirms baseline thresholds (precision ≥ 0.9, recall ≥ 0.7)
- [ ] If thresholds fail, tune dedup pipeline and re-evaluate before moving to Phase 2
- [ ] Commit corpus snapshot as `tests/fixtures/posts_corpus_v1.jsonl` (avoid committing user-identifying or copyrighted content; keep texts short and paraphrased if needed)

## Exit criteria for Phase 1

- [ ] Bot has been running for 14 consecutive days without manual intervention
- [ ] At least 70% of "important" events from the dogfooding log were delivered in a digest
- [ ] Less than 10% of digest items rated as "spam" or "irrelevant"
- [ ] LLM cost stayed within monthly budget
- [ ] No session-reader bans
- [ ] Golden dataset eval passes baseline thresholds

Phase 1 is done when all six exit criteria are met. Proceed to Phase 2 (multi-user + Free/Pro + TG Stars).
