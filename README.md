# mute-bot

Telegram news digest bot. Reads public channels via MTProto, deduplicates events, ranks by importance, delivers per-user digests.

See `docs/superpowers/specs/2026-06-03-news-digest-bot-design.md` for the spec.

## Local dev

```bash
make docker-up        # postgres, redis, nats
make migrate-up
make test
```
