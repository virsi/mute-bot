# News Digest Telegram Bot — Design Spec

**Дата:** 2026-06-03
**Статус:** Draft, brainstorming finalized
**Автор:** vir$i
**Цель документа:** функциональные и архитектурные требования к боту-агрегатору новостей с дедупликацией, ранжированием и тематическим разделением.

---

## 1. Контекст и мотивация

Пользователь следит за десятками публичных Telegram-каналов с новостями. Боль: дублирование одной и той же новости в нескольких пабликах, переменное качество подачи, шум при отсутствии важных событий. Цель — единый бот-агрегатор, который:

- читает заранее заданный пул каналов;
- схлопывает дубликаты в события (кластеры);
- ранжирует события по важности;
- доставляет сводки по расписанию, по запросу и срочными пушами;
- поддерживает тематическое разделение и тонкую настройку под юзера.

Конечная модель — SaaS-бот с подпиской. MVP-фаза 1 — для собственного использования, фаза 2 — мульти-юзер с тарификацией.

---

## 2. Дорожная карта по фазам

### Фаза 1 — Solo-mode (2–4 недели)
Один юзер (автор). Цель — отладить ядро дедупа и ранжирования на реальных данных.

**Включено:**
- MTProto user-session читает фиксированный пул каналов из конфига.
- Полный pipeline: normalize → dedup → classify → score.
- 5–7 предзаданных тем.
- Утренний и вечерний digest по cron.
- Команды: `/start`, `/digest`, `/settings`, `/topics`, `/threshold`.
- Anti-repeat: один кластер не доставляется юзеру дважды.

**Не включено:** платежи, real-time alerts, кастомные темы, кастомные источники, weekly digest, тиры.

### Фаза 2 — Subscription layer (1–2 недели)
Мульти-юзер, монетизация.

**Добавлено:**
- Профили юзеров в БД.
- Free / Pro разделение (feature flags).
- TG Stars через абстракцию `PaymentProvider`.
- Кастомные темы (Pro): юзер описывает тему текстом, LLM использует описание как промпт классификации.
- Real-time alerts (Pro): пуш при превышении порога важности.

### Фаза 3 — Pro-features expansion (1–2 недели)
Расширение функционала Pro.

**Добавлено:**
- Кастомные источники (Pro): юзер добавляет/блокирует конкретные каналы. Динамическая подписка сессии. Лимит каналов на тариф.
- Weekly digest.
- Второй платежный провайдер (ЮКасса/СБП).
- Админ-инструменты: `/dlq`, ручная курация `channels.authority_score`.

---

## 3. Функциональные требования

### 3.1 Источники данных
- **FR-1.1** Бот читает посты из публичных Telegram-каналов через MTProto user-session.
- **FR-1.2** Доставка юзерам — через отдельный Bot API процесс (раздельные runtime, разные риски).
- **FR-1.3** Catch-up при рестарте: сессия дочитывает пропущенные сообщения, начиная с последнего обработанного `tg_msg_id` на канал.
- **FR-1.4** В фазе 1 пул каналов задаётся конфигом. В фазе 3 — расширяется per-user добавлением.

### 3.2 Дедупликация
- **FR-2.1** Посты разных каналов про одно событие схлопываются в один кластер.
- **FR-2.2** Pipeline: MinHash similarity (cheap-фильтр) → embeddings + kNN (качественная близость) → LLM-merge для пограничных случаев (порог 0.75–0.85 cosine).
- **FR-2.3** Окно дедупа: 48 часов с момента первого поста кластера.
- **FR-2.4** Целевые метрики на golden dataset: precision ≥ 0.9, recall ≥ 0.7.

### 3.3 Ранжирование важности
- **FR-3.1** Каждый кластер получает score:
  ```
  score = w_cov · log(distinct_channels + 1)
        + w_auth · max(channel_authority)
        + w_sev · llm_severity / 100
  ```
- **FR-3.2** Веса в конфиге, дефолт: `w_cov=0.4`, `w_auth=0.3`, `w_sev=0.3` (тюнятся на дата-фазе).
- **FR-3.3** `channel_authority` — ручная курация (0–100), плюс автоматический сигнал от числа подписчиков.
- **FR-3.4** `llm_severity` — оценка LLM 0–100 по содержанию (срочность, масштаб, последствия).
- **FR-3.5** Views/reactions/forwards **не используются** (накручиваются).

### 3.4 Темы
- **FR-4.1** Системные пресеты (фаза 1): `politics`, `it`, `crypto`, `economy`, `war`, `science`, `sport`. Финальный список тюнится по результатам dogfooding.
- **FR-4.2** Multi-label: один кластер может попасть в несколько тем.
- **FR-4.3** Кастомные темы (Pro, фаза 2): юзер задаёт `name` + текстовое `description`, LLM-классификатор использует описание как промпт.
- **FR-4.4** Кластер без подходящих тем попадает в синтетическую «Прочее».

### 3.5 Доставка
- **FR-5.1** Четыре канала доставки: scheduled digest, real-time alerts, on-demand `/digest`, weekly digest.
- **FR-5.2** Scheduled digest: юзер задаёт массив времён + tz. Дефолт `["08:00", "19:00"]` Europe/Moscow.
- **FR-5.3** Real-time alerts (Pro, фаза 2): пуш при `score ≥ user.alert_threshold`. Throttle: ≤ 1 alert на тему за 30 минут.
- **FR-5.4** On-demand `/digest`: возвращает топ-N кластеров с момента последней доставки.
- **FR-5.5** Weekly digest (Pro, фаза 3): один раз в неделю, понедельник 09:00 локально юзеру, топ событий по всем темам юзера за 7 дней.
- **FR-5.6** Anti-repeat: кластер, попавший в `deliveries(user_id, cluster_id)`, не отправляется этому юзеру повторно в любых каналах доставки.

### 3.6 Формат сообщения
- **FR-6.1** Сводка = одно сообщение (или несколько, при превышении TG-лимита 4096 символов), сгруппированное по темам.
- **FR-6.2** Каждый элемент содержит:
  - Заголовок (LLM-generated, ≤ 100 символов).
  - Summary 2–3 строки (LLM-generated).
  - Список источников: упоминания каналов через `@username`, до 5 самых релевантных.
- **FR-6.3** Шаблон:
  ```
  🗞 Утренняя сводка · 03 июня

  📌 Политика
  1. [Заголовок]
     [Summary 2-3 строки.]
     📡 @ria, @mash, @brief

  2. ...

  📌 IT
  ...
  ```
- **FR-6.4** Если кластеров после фильтра порогом нет — сводка не отправляется (или отправляется только при on-demand).

### 3.7 Настройки юзера
- **FR-7.1** Подписка/отписка от тем (вкл/выкл пресета, добавление кастома в Pro).
- **FR-7.2** Глобальный порог важности (0–100). Кластеры со `score < threshold` не попадают в digest.
- **FR-7.3** Расписание digest: набор времён + таймзона.
- **FR-7.4** Alert threshold отдельно (только Pro): срочный порог, обычно выше digest-порога.
- **FR-7.5** Кастомные источники (Pro, фаза 3): добавление до N каналов (N зависит от тарифа), блокировка системных каналов.
- **FR-7.6** Команды: `/start`, `/digest`, `/topics`, `/threshold`, `/schedule`, `/alerts` (Pro), `/sources` (Pro), `/buy` (Pro), `/settings` (главное меню), `/help`.

### 3.8 Подписка и монетизация (фаза 2+)
- **FR-8.1** Два тира: `free`, `pro`.
- **FR-8.2** Free ограничения:
  - До 3 активных тем (только из пресетов).
  - До 2 времён digest в сутки.
  - Только пресет-источники.
  - Без alerts, без weekly, без кастом-тем.
- **FR-8.3** Pro:
  - Безлимит тем (включая кастомные).
  - Безлимит времён digest.
  - Alerts + weekly.
  - Кастомные источники до N каналов (фаза 3, N конкретизируем при запуске).
- **FR-8.4** Подписка покупается через `PaymentProvider` интерфейс. Реализации: TG Stars (фаза 2), ЮКасса (фаза 3).
- **FR-8.5** Истечение подписки: переход в free, лишние темы/источники деактивируются (не удаляются), при апгрейде восстанавливаются.
- **FR-8.6** Идемпотентность платежей: `UNIQUE(provider, provider_ref)` в `subscriptions`.

---

## 4. Архитектура

### 4.1 High-level

```
┌───────────────────────┐        ┌──────────────────────┐
│  MTProto User-Session │        │  Bot API (delivery)  │
│  чтение каналов       │        │  ответы юзерам       │
└──────────┬────────────┘        └───────────┬──────────┘
           │ raw posts                       │ commands/digest
           ▼                                 ▲
┌───────────────────────┐        ┌──────────────────────┐
│  Ingest queue (NATS / │        │  Digest assembler    │
│  Redis Streams)       │        │  (per-user filter)   │
└──────────┬────────────┘        └───────────┬──────────┘
           │                                 │
           ▼                                 │
┌─────────────────────────────────────────────────────────┐
│  Pipeline: Normalize → Dedup → Classify → Rank          │
│  (stateless workers, JetStream consumers)               │
└──────────────────────────┬──────────────────────────────┘
                           ▼
                ┌──────────────────────────────┐
                │  Postgres + pgvector + Redis │
                └──────────────────────────────┘
                           ▲
                           │
                ┌──────────────────────────────┐
                │  Scheduler (cron) / Alerter  │
                └──────────────────────────────┘
```

### 4.2 Принципы
- **Sessions ≠ bot** — раздельные процессы. Бан сессии не валит бот.
- **Multi-tenant с фазы 1** — все запросы проходят через `user_id`, даже если он один.
- **Idempotent pipeline** — любое сообщение можно проиграть N раз без побочек.
- **Degraded mode > downtime** — при частичных сбоях бот шлёт менее качественную, но валидную сводку.
- **Provider-agnostic payments** — `PaymentProvider` интерфейс с первой реализации.

---

## 5. Компоненты

### 5.1 `session-reader`
Держит MTProto-сессию, слушает updates, нормализует TG-сообщения в `RawPost`, публикует в `ingest.raw`. Сохраняет per-channel `last_seen_msg_id` для catch-up. При бане сессии — алерт + fallback на резервную сессию (если есть).

### 5.2 `normalizer`
Чистит текст (эмодзи, реклама-хвосты, ссылки), детектит язык, считает length. Stateless. Публикует `NormalizedPost` в `ingest.normalized`, делает INSERT в `posts`.

### 5.3 `dedup-worker`
Пайплайн внутри:
1. MinHash signature → проверка против Redis MinHash-индекса (окно 48ч).
2. Hit (Jaccard > 0.85) → attach к существующему кластеру.
3. Miss → запрос embedding (через `embeddings-service`, batched).
4. kNN в pgvector в окне 48ч.
5. Cosine > 0.85 → attach. 0.75–0.85 → отложить на LLM-merge батч.
6. Иначе — новый кластер.

Output: UPDATE `posts.cluster_id`, INSERT `clusters` при необходимости, publish `cluster.updated`.

### 5.4 `embeddings-service`
Генерирует векторы. Batch API. Дефолт — OpenAI `text-embedding-3-small` (1536d). Альтернатива — локальный `multilingual-e5-base` (CPU). Redis-кэш по `text_hash → embedding`, TTL 7 дней.

### 5.5 `classifier`
Слушает `cluster.updated`, дебаунс 60с (ждёт «дозревания» кластера). LLM-промпт включает описания пресетов + кастом-тем активных юзеров. Возвращает `topics[]`, `severity (0-100)`, `headline`, `summary`. Пишет в `clusters`.

### 5.6 `ranker`
Пересчитывает `score` при `cluster.updated`. Публикует `cluster.scored`.

### 5.7 `digest-assembler`
По триггеру `delivery.scheduled` или команде `/digest`:
1. Загружает `user_settings`.
2. Выбирает кластеры: `topic ∈ user.topics AND score ≥ threshold AND id NOT IN deliveries[user] AND created_at ≥ since`.
3. Сортирует по score, режет до limit.
4. Группирует по темам, форматит.
5. Отправляет через Bot API.
6. INSERT в `deliveries`.

### 5.8 `scheduler`
Cron-триггеры на digest. Распределённый lock в Postgres (advisory lock) защищает от двойной отправки при нескольких инстансах.

### 5.9 `alerter` (фаза 2)
Слушает `cluster.scored`. Если `score ≥ user.alert_threshold` и юзер Pro — пуш. Throttle: ≤ 1 alert на тему за 30 минут на юзера.

### 5.10 `bot-api`
Обрабатывает команды. Stateless, всё через БД.

### 5.11 `payments` (фаза 2)
Интерфейс:
```
PaymentProvider {
  CreateInvoice(user_id, plan, period) → invoice_url
  HandleWebhook(payload) → PaymentEvent
}
```
Реализации: `tg_stars` (фаза 2), `yookassa` (фаза 3). Активация подписки централизованно через таблицу `subscriptions`.

---

## 6. Данные

### 6.1 Postgres схема

```sql
channels (
  id              bigserial PK,
  tg_channel_id   bigint UNIQUE NOT NULL,
  username        text,
  title           text,
  authority_score int DEFAULT 50,
  added_by        bigint NULL,
  active          bool DEFAULT true,
  created_at      timestamptz
)

posts (
  id              bigserial PK,
  channel_id      bigint FK channels,
  tg_msg_id       bigint NOT NULL,
  text_raw        text,
  text_clean      text,
  text_hash       bytea,
  lang            text,
  posted_at       timestamptz,
  ingested_at     timestamptz,
  cluster_id      bigint NULL FK clusters,
  UNIQUE(channel_id, tg_msg_id)
)
CREATE INDEX ON posts(posted_at);
CREATE INDEX ON posts(cluster_id);

post_embeddings (
  post_id    bigint PK FK posts,
  embedding  vector(1536),
  model      text
)
CREATE INDEX ON post_embeddings USING ivfflat (embedding vector_cosine_ops);

clusters (
  id              bigserial PK,
  headline        text,
  summary         text,
  topics          text[] NOT NULL,
  severity        int,
  coverage        int DEFAULT 1,
  score           real,
  first_seen_at   timestamptz,
  last_updated_at timestamptz,
  status          text DEFAULT 'active'
)
CREATE INDEX ON clusters(last_updated_at, score DESC);
CREATE INDEX ON clusters USING gin(topics);

users (
  id              bigserial PK,
  tg_user_id      bigint UNIQUE,
  tg_username     text,
  tier            text DEFAULT 'free',
  tier_until      timestamptz NULL,
  lang            text DEFAULT 'ru',
  blocked         bool DEFAULT false,
  created_at      timestamptz
)

user_settings (
  user_id          bigint PK FK users,
  topics           text[] NOT NULL,
  threshold        int DEFAULT 50,
  digest_schedule  jsonb,
  alerts_enabled   bool DEFAULT false,
  alert_threshold  int DEFAULT 85,
  weekly_enabled   bool DEFAULT false,
  updated_at       timestamptz
)

custom_topics (
  id          bigserial PK,
  user_id     bigint FK users,
  name        text,
  description text,
  created_at  timestamptz
)

user_channels (
  user_id     bigint FK users,
  channel_id  bigint FK channels,
  blocked     bool DEFAULT false,
  PRIMARY KEY(user_id, channel_id)
)

deliveries (
  id           bigserial PK,
  user_id      bigint FK users,
  cluster_id   bigint FK clusters,
  delivered_at timestamptz,
  channel      text,
  UNIQUE(user_id, cluster_id)
)
CREATE INDEX ON deliveries(user_id, delivered_at DESC);

subscriptions (
  id             bigserial PK,
  user_id        bigint FK users,
  plan           text,
  provider       text,
  provider_ref   text,
  amount         int,
  currency       text,
  started_at     timestamptz,
  expires_at     timestamptz,
  status         text,
  UNIQUE(provider, provider_ref)
)
```

### 6.2 Redis ключи

| Ключ | Значение | TTL |
|---|---|---|
| `minhash:idx` | LSH-индекс хешей за 48ч | rolling |
| `emb:{text_hash}` | embedding vector | 7д |
| `cluster_warm:{id}` | hot cluster JSON | 1ч |
| `rate:{user_id}:{action}` | rate limit counter | по контексту |
| `alert_throttle:{user_id}:{topic}` | last alert ts | 30мин |

### 6.3 Очереди (NATS JetStream / Redis Streams)

| Topic | Producer | Consumer | Payload |
|---|---|---|---|
| `ingest.raw` | session-reader | normalizer | RawPost |
| `ingest.normalized` | normalizer | dedup-worker | NormalizedPost |
| `cluster.updated` | dedup-worker | classifier, ranker | `{cluster_id}` |
| `cluster.scored` | ranker | alerter | `{cluster_id, score, topics}` |
| `delivery.scheduled` | scheduler | digest-assembler | `{user_id, channel}` |

Все consumers — `ack_explicit`, retries до 5, далее `*.dlq`.

### 6.4 Retention

- `posts`, `clusters` старше 30 дней → archived (отдельная таблица или partition).
- `post_embeddings` старше 7 дней без активного кластера → удалить.
- `deliveries` хранятся бессрочно (~ 16 байт на запись).

---

## 7. Обработка ошибок

### 7.1 Классы сбоев

| Класс | Пример | Стратегия |
|---|---|---|
| Transient | таймаут OpenAI, JetStream nack | retry с backoff |
| Rate-limited | TG FLOOD_WAIT, OpenAI 429 | backoff из header, throttle |
| Bad data | пост без текста | drop с warn |
| External outage | OpenAI down | circuit breaker → degraded mode |
| Account ban | сессия забанена | алерт + fallback или пауза |
| Bug | паника воркера | restart + DLQ |

### 7.2 Идемпотентность

| Операция | Ключ |
|---|---|
| INSERT post | UNIQUE(channel_id, tg_msg_id) |
| Create cluster | advisory_lock(text_hash) |
| Deliver digest | UNIQUE(user_id, cluster_id) |
| Payment activation | UNIQUE(provider, provider_ref) |
| Cluster score recalc | UPDATE с WHERE last_updated_at < new_ts |

### 7.3 Circuit breakers

| Сервис | Open trigger | Degraded behavior |
|---|---|---|
| LLM | 50% errors / 1min | кластер без тем/severity → «Прочее», headline = первое предложение топ-поста |
| Embeddings | 50% errors / 1min | дедуп только по MinHash |
| Postgres | connection refused | session-reader → disk-buffer, workers пауза |
| Bot API | 50% errors / 1min | очередь отправки, retry до 6ч, потом drop |

### 7.4 Лимиты и backpressure

| Ресурс | Лимит | Действие |
|---|---|---|
| OpenAI tokens/min | конфиг | Redis token bucket |
| TG Bot API | 30 msg/s global, 1/s per chat | очередь |
| MTProto reads | TG dynamic | sliding-window backoff |
| dedup-worker queue | > 10k depth | scale + alert |
| User commands | 10/min/user | per-user rate limit |

### 7.5 Наблюдаемость

**Логи:** structured JSON с `trace_id`, `user_id`, `cluster_id`, `component`, `level`.

**Метрики (Prometheus):**
- `ingest_posts_total{channel}`, `posts_dropped_total{reason}`
- `dedup_match_kind_total{kind=minhash|emb|llm|new}`
- `cluster_size_bucket`, `cluster_lifetime_seconds`
- `llm_calls_total{purpose}`, `llm_tokens_total{purpose}`, `llm_cost_usd_total`
- `digest_sent_total{tier,channel}`, `digest_assemble_seconds`
- `subscription_active_gauge{tier}`
- `cb_state{component}`, `queue_depth{stream}`

**Алерты:** session ban, DLQ depth > 0, pipeline lag > 5мин, LLM cost > daily budget × 1.5, CB open > 5мин.

**Tracing:** OpenTelemetry, trace_id от поста до доставки.

### 7.6 Юзер-facing

- Команда падает → `⚠️ Не удалось выполнить. Попробуйте позже.`
- Платёж в `pending` > 10мин → `/start` показывает «проверяем платёж».
- Сбои дедупа/классификации **никогда** не видны юзеру (degraded mode тихо работает).

---

## 8. Тестирование

### 8.1 Уровни

- **Unit:** normalizer cleanup, ranker формула, digest-assembler фильтры, MinHash/SimHash, PaymentProvider парсинг, rate limiter.
- **Integration (testcontainers):** dedup на «5 пабликов одной новости», classifier со stubbed LLM (JSON fixtures), полный delivery flow, anti-repeat, subscription activation.
- **Contract:** TG MTProto cassettes, Bot API mock-server, OpenAI replay (`vcrpy`/`go-vcr`), Stars webhook fixtures.
- **E2E:** «канал постит → юзер получает digest» сценарий, burst-load.

### 8.2 Качественная оценка (golden dataset)

- 200–500 реальных постов из 20 каналов за неделю, размечены вручную: пары «одно событие?» и шкала важности 1–5.
- Хранится в `tests/fixtures/news_corpus_v1.jsonl`.
- Метрики дедупа: precision ≥ 0.9, recall ≥ 0.7, adjusted Rand index.
- Метрики ранжирования: nDCG@10, Spearman correlation.
- Baselines в `tests/baselines/dedup_metrics.json`, CI fail при деградации > 5%.

### 8.3 LLM-тестирование

- Промпты в файлах с версиями: `prompts/classifier.v3.txt`.
- Каждое изменение промпта → прогон golden dataset.
- Snapshot-тесты на типичные ответы.
- Budget-тест: `tokens_per_post < N`, иначе CI fail.

### 8.4 Нагрузочные сценарии

- Steady: 1500 постов/час.
- Burst: 200 постов/мин.
- Доставки: 60k/день (1000 юзеров × 2 digest).
- Цели: p95 ingest→cluster ≤ 60s, p99 digest ≤ 5s, LLM cost ≤ $50/мес на 100 активных юзеров.

### 8.5 Безопасность

- Fuzz парсера TG-сообщений.
- SQL-injection тесты в username полях.
- Webhook signature verification.
- Multi-account abuse: фиксация по `tg_user_id`.
- Secrets: Vault/Doppler, ротация раз в квартал.
- Логи не содержат текст постов целиком (хеши и метаданные).

### 8.6 Manual QA

- Автор — единственный юзер фазы 1. Дневник «пропустил/спам/недодеда» 2–4 недели.
- Еженедельная сверка с golden dataset, пополнение корпуса.

### 8.7 CI

```
lint → unit → integration → contract → dedup eval → build → deploy staging → smoke E2E
```

### 8.8 Что НЕ тестируем

- Внешний TG API, OpenAI ответы дословно, внутрянку MTProto-клиента, UI-скриншоты (валидируем структуру).

---

## 9. Открытые вопросы

- **Q1:** Финальный список пресет-тем — уточняется после 1-2 недель dogfooding на реальных данных.
- **Q2:** Конкретные веса формулы `score` — тюнятся на golden dataset.
- **Q3:** Лимит N кастомных каналов для Pro в фазе 3 — зависит от capacity сессии и стоимости LLM.
- **Q4:** Цены Pro подписки — определяются перед запуском фазы 2.
- **Q5:** Язык: фаза 1 — только русский. Мультиязычность отложить.
- **Q6:** Стек реализации (Go vs Python) — решается в плане имплементации.

---

## 10. Глоссарий

- **Кластер** — группа постов из разных каналов про одно событие.
- **Coverage** — число различных каналов в кластере.
- **Authority** — рейтинг канала (0–100), курируется вручную.
- **Severity** — LLM-оценка важности содержания (0–100).
- **Score** — финальная важность кластера, взвешенная сумма coverage/authority/severity.
- **Digest** — сводка топ-кластеров за период.
- **Alert** — внеплановый пуш кластера высокой важности.
- **Anti-repeat** — механизм не доставлять один кластер юзеру дважды.
