// Command bot-api is the Telegram Bot API process. It registers handlers
// for the slash commands defined in internal/bot/commands.go and runs the
// go-telegram/bot long-polling loop. The /digest command delegates to a
// digest.Assembler so on-demand digests share the same pipeline as the
// scheduled deliveries produced by cmd/scheduler.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/virsi/mute-bot/internal/billing"
	"github.com/virsi/mute-bot/internal/bot"
	"github.com/virsi/mute-bot/internal/config"
	"github.com/virsi/mute-bot/internal/digest"
	"github.com/virsi/mute-bot/internal/llm"
	"github.com/virsi/mute-bot/internal/obs"
	"github.com/virsi/mute-bot/internal/storage/postgres"
	"github.com/virsi/mute-bot/internal/topics"
	"github.com/virsi/mute-bot/internal/users"
)

func main() {
	if err := run(); err != nil {
		slog.Error("bot-api: fatal", slog.Any("err", err))
		os.Exit(1)
	}
}

func run() error {
	cfgPath := flag.String("config", "configs/config.yaml", "path to config yaml")
	flag.Parse()

	slog.SetDefault(obs.NewLogger(slog.LevelInfo, "bot-api"))

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// HTTP slot on :9103. Hosts /metrics + /healthz; the YooKassa webhook
	// /yookassa/webhook is mounted later once the billing.Service is
	// built. Building http.Server here lets the same listener serve all
	// three paths so deployments do not need extra ports for billing.
	httpMux := http.NewServeMux()
	httpMux.Handle("/metrics", promhttp.Handler())
	httpMux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	httpSrv := &http.Server{
		Addr:              ":9103",
		Handler:           httpMux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	// Start the listener after we have wired the YooKassa webhook below.
	defer func() { _ = httpSrv.Close() }()

	rootCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	shutdownTracing, err := obs.SetupTracing(rootCtx, "bot-api", cfg.OTLPEndpoint)
	if err != nil {
		return fmt.Errorf("setup tracing: %w", err)
	}
	defer func() {
		shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelShutdown()
		_ = shutdownTracing(shutdownCtx)
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-stop
		slog.Info("bot-api: shutdown signal", slog.String("sig", sig.String()))
		cancel()
	}()

	pool, err := postgres.NewPool(rootCtx, cfg.PostgresDSN)
	if err != nil {
		return fmt.Errorf("pg pool: %w", err)
	}
	defer pool.Close()

	client, err := bot.NewClient(cfg.BotToken)
	if err != nil {
		return fmt.Errorf("new bot client: %w", err)
	}
	// Sender wraps the bot client with per-chat rate limiting for digest
	// pushes. Slash-command replies bypass it — they go straight through
	// the client's Send because user-facing latency matters more than
	// back-pressure at one message per command invocation.
	sender := bot.NewSender(bot.SenderDeps{API: &client.SendOnly, PerChatPerSec: 1})

	usersRepo := postgres.NewUsersRepo(pool)
	settings := postgres.NewSettingsRepo(pool)
	clusters := postgres.NewClustersRepo(pool)
	deliveries := postgres.NewDeliveriesRepo(pool)
	channelsRepo := postgres.NewChannelsRepo(pool)
	subsRepo := postgres.NewSubscriptionsRepo(pool)
	userTopicsRepo := postgres.NewUserTopicsRepo(pool)

	// usersSvc unifies /start, billing webhooks (M3) and the expiry sweep
	// behind one service so the upsert + default-seed flow stays in one
	// place. It also doubles as the TierChecker for /settings and the
	// Pro-gate middleware below.
	usersSvc := users.NewService(users.Deps{Users: usersRepo, Settings: settings})

	// LLM client for the topics.Service AddTopic embedding call. The
	// budget guard is shared with the processor's pipeline so the
	// monthly cap covers both the dedup embedder and the user-topic
	// embedder. INV-5: embed is paid once per /topics add — never per
	// digest assembly (the digest filter only does cosine math in SQL).
	llmBudget := llm.NewBudgetGuard(llm.BudgetConfig{MonthlyUSD: cfg.LLM.MonthlyBudgetUSD})
	llmClient := llm.NewOpenAI(llm.OpenAIConfig{
		APIKey:  cfg.OpenAIAPIKey,
		BaseURL: cfg.LLM.BaseURL,
		Budget:  llmBudget,
	})
	topicsSvc := topics.NewService(topics.Deps{
		Repo:     userTopicsRepo,
		Embedder: llmClient,
		Model:    cfg.LLM.EmbeddingModel,
	})

	// Billing wiring: StarsProvider over the live Bot API. YooKassa is
	// optional — only enabled when shop_id + secret_key + base_external_url
	// AND webhook_secret are all set. Treating webhook_secret as required
	// the moment YooKassa is enabled avoids the failure mode where the
	// renewer charges saved cards but the matching webhook handler stays
	// unmounted (yielding 404 for every payment.succeeded notification and
	// leaving paid users on the Free tier).
	starsProvider := billing.NewStarsProvider(client.Bot())
	providers := map[string]billing.Provider{"tg_stars": starsProvider}
	var ykProvider *billing.YooKassaProvider
	ykConfigured := cfg.YooKassa.ShopID != "" || cfg.YooKassa.SecretKey != "" ||
		cfg.YooKassa.WebhookSecret != ""
	if ykConfigured {
		if cfg.YooKassa.ShopID == "" || cfg.YooKassa.SecretKey == "" ||
			cfg.YooKassa.WebhookSecret == "" || cfg.BaseExternalURL == "" {
			return fmt.Errorf("yookassa: shop_id, secret_key, webhook_secret and base_external_url must all be set together; got shop_id=%t secret_key=%t webhook_secret=%t base_external_url=%t",
				cfg.YooKassa.ShopID != "",
				cfg.YooKassa.SecretKey != "",
				cfg.YooKassa.WebhookSecret != "",
				cfg.BaseExternalURL != "",
			)
		}
		ykProvider = billing.NewYooKassaProvider(billing.YooKassaDeps{
			ShopID:     cfg.YooKassa.ShopID,
			SecretKey:  cfg.YooKassa.SecretKey,
			WebhookURL: cfg.BaseExternalURL + "/yookassa/webhook",
			ReturnURL:  cfg.YooKassa.ReturnURL,
		})
		providers["yookassa"] = ykProvider
		slog.Info("bot-api: yookassa provider enabled",
			slog.String("webhook_url", cfg.BaseExternalURL+"/yookassa/webhook"))
	}
	billingSvc := billing.NewService(billing.Deps{
		Providers: providers,
		Subs:      subsRepo,
		Users:     usersSvc,
	})
	paymentHandlers := bot.NewPaymentHandlers(bot.PaymentHandlersDeps{
		Acker:   client.Bot(),
		Settler: billingSvc,
		API:     &client.SendOnly,
	})
	// Attach the YooKassa webhook to the shared HTTP mux. The startup
	// guard above guarantees WebhookSecret is non-empty whenever the
	// provider is enabled, so this branch fully commits the route or the
	// provider was never instantiated in the first place.
	if ykProvider != nil {
		httpMux.Handle("/yookassa/webhook", billing.NewYooKassaWebhook(billing.YooKassaWebhookDeps{
			Settler: billingSvc,
			Secret:  cfg.YooKassa.WebhookSecret,
		}))
	}
	// Start the HTTP listener now that all routes are mounted.
	go func() {
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("bot-api: http server error", slog.Any("err", err))
		}
	}()

	// YooKassa autopayment renewer. Scans every hour for subscriptions
	// that expire within 24h and have a saved payment_method_id, then
	// charges them via /v3/payments — the resulting payment.succeeded
	// webhook flows back through the same Settle path, granting +30 days
	// idempotently. Stars users have no saved card so they are skipped
	// at the SQL level.
	if ykProvider != nil {
		renewer := billing.NewYooKassaRenewer(billing.YooKassaRenewerDeps{
			Renewer:  ykProvider,
			Subs:     subsRepo,
			Interval: time.Hour,
			Window:   24 * time.Hour,
		})
		go func() {
			if err := renewer.Run(rootCtx); err != nil && !errors.Is(err, context.Canceled) {
				slog.Error("bot-api: yookassa renewer", slog.Any("err", err))
			}
		}()
	}

	assembler := digest.NewAssembler(digest.AssemblerDeps{
		Settings:   settings,
		Clusters:   clusters,
		Deliveries: deliveries,
		Sources:    channelsRepo,
		Sender:     sender,
	})
	// Translate the bot-side request shape into digest.AssembleRequest. Keeps
	// the bot package free of digest types and the digest package free of bot
	// types — the only coupling is this one closure.
	asmAdapter := bot.AssemblerFunc(func(ctx context.Context, r bot.AssembleReq) error {
		return assembler.Assemble(ctx, digest.AssembleRequest{
			UserID:   r.UserID,
			TGUserID: r.TGUserID,
			Channel:  r.Channel,
			Title:    r.Title,
		})
	})

	// Weekly digest wiring: shares the daily collaborators but uses its
	// own TopByScoreSince + anti-repeat repo. Anti-repeat is now a hard
	// once-per-week guard on both the on-demand /weekly path and the
	// Sunday cron path — they consult the same HasWeekRow marker so the
	// user cannot receive two copies in the same ISO week regardless of
	// which surface fires first.
	weeklyRepo := postgres.NewWeeklyDeliveriesRepo(pool)
	weeklyAssembler := digest.NewWeeklyAssembler(digest.WeeklyAssemblerDeps{
		Settings:     settings,
		Clusters:     clusters,
		Weekly:       weeklyRepo,
		Sources:      channelsRepo,
		Sender:       sender,
		Tier:         usersSvc,
		Users:        usersRepo,
		CustomTopics: topicsSvc,
		Centroider:   postgres.NewEmbeddingsRepo(pool),
	})
	weeklyAdapter := bot.WeeklyAssemblerFunc(func(ctx context.Context, u, tg int64) error {
		return weeklyAssembler.BuildWeekly(ctx, digest.WeeklyRequest{UserID: u, TGUserID: tg})
	})

	h := bot.NewHandlers(bot.HandlersDeps{
		Users:     usersRepo,
		Settings:  settings,
		Assembler: asmAdapter,
		API:       &client.SendOnly,
		Registrar: usersSvc,
		Tier:      usersSvc,
		Invoicer:  billingSvc,
		ButtonAPI: &client.SendOnly,
		TwoButton: &client.SendOnly,
		Topics:    topicsSvc,
		Weekly:    weeklyAdapter,
	})

	registerHandlers(client.Bot(), h, usersSvc, &client.SendOnly)
	registerPaymentHandlers(client.Bot(), paymentHandlers)

	slog.Info("bot-api: starting long poll")
	client.Bot().Start(rootCtx)
	slog.Info("bot-api: stopped")
	return nil
}

// registerHandlers attaches one prefix handler per slash command. The
// go-telegram/bot router dispatches by prefix-match on Message.Text, so the
// handlers themselves are responsible for parsing trailing arguments.
//
// Errors returned from h.Handle* are logged and swallowed: a single failing
// command should not stop the long-poll loop.
//
// reg + sender are accepted so M4/M5 can wrap newly-added Pro-only
// commands with bot.RequirePro(reg, reg.(bot.TierChecker), sender, ...)
// without further plumbing. M2 itself ships only Free-accessible
// commands plus the /buy stub.
func registerHandlers(b *tgbot.Bot, h *bot.Handlers, reg bot.Registrar, sender bot.SendAPI) {
	tier, _ := reg.(bot.TierChecker) // users.Service satisfies both ports

	b.RegisterHandler(tgbot.HandlerTypeMessageText, "/start", tgbot.MatchTypePrefix,
		func(ctx context.Context, _ *tgbot.Bot, u *models.Update) {
			if u.Message == nil || u.Message.From == nil {
				return
			}
			if err := h.HandleStart(ctx, u.Message.From.ID, u.Message.From.Username); err != nil {
				slog.Error("/start failed", slog.Any("err", err))
			}
		},
	)
	b.RegisterHandler(tgbot.HandlerTypeMessageText, "/digest", tgbot.MatchTypePrefix,
		func(ctx context.Context, _ *tgbot.Bot, u *models.Update) {
			if u.Message == nil || u.Message.From == nil {
				return
			}
			if err := h.HandleDigest(ctx, u.Message.From.ID, u.Message.From.Username); err != nil {
				slog.Error("/digest failed", slog.Any("err", err))
			}
		},
	)
	b.RegisterHandler(tgbot.HandlerTypeMessageText, "/settings", tgbot.MatchTypePrefix,
		func(ctx context.Context, _ *tgbot.Bot, u *models.Update) {
			if u.Message == nil || u.Message.From == nil {
				return
			}
			if err := h.HandleSettings(ctx, u.Message.From.ID, u.Message.From.Username); err != nil {
				slog.Error("/settings failed", slog.Any("err", err))
			}
		},
	)
	b.RegisterHandler(tgbot.HandlerTypeMessageText, "/threshold", tgbot.MatchTypePrefix,
		func(ctx context.Context, _ *tgbot.Bot, u *models.Update) {
			if u.Message == nil || u.Message.From == nil {
				return
			}
			parts := strings.Fields(u.Message.Text)
			if len(parts) < 2 {
				// No arg given — show current settings instead of erroring.
				if err := h.HandleSettings(ctx, u.Message.From.ID, u.Message.From.Username); err != nil {
					slog.Error("/threshold (no arg) failed", slog.Any("err", err))
				}
				return
			}
			n, err := strconv.Atoi(parts[1])
			if err != nil {
				slog.Warn("/threshold: bad number", slog.String("arg", parts[1]))
				return
			}
			if err := h.HandleThreshold(ctx, u.Message.From.ID, u.Message.From.Username, n); err != nil {
				slog.Error("/threshold failed", slog.Any("err", err))
			}
		},
	)
	b.RegisterHandler(tgbot.HandlerTypeMessageText, "/schedule", tgbot.MatchTypePrefix,
		func(ctx context.Context, _ *tgbot.Bot, u *models.Update) {
			if u.Message == nil || u.Message.From == nil {
				return
			}
			parts := strings.Fields(u.Message.Text)
			// /schedule with no args — fall back to /settings so the user
			// can see the current schedule before editing it.
			if len(parts) < 2 {
				if err := h.HandleSettings(ctx, u.Message.From.ID, u.Message.From.Username); err != nil {
					slog.Error("/schedule (no arg) failed", slog.Any("err", err))
				}
				return
			}
			timesCSV := parts[1]
			tz := ""
			if len(parts) >= 3 {
				tz = parts[2]
			}
			if err := h.HandleSchedule(ctx, u.Message.From.ID, u.Message.From.Username, timesCSV, tz); err != nil {
				slog.Error("/schedule failed", slog.Any("err", err))
			}
		},
	)
	b.RegisterHandler(tgbot.HandlerTypeMessageText, "/topics", tgbot.MatchTypePrefix,
		func(ctx context.Context, _ *tgbot.Bot, u *models.Update) {
			if u.Message == nil || u.Message.From == nil {
				return
			}
			parts := strings.Fields(u.Message.Text)
			if len(parts) < 2 {
				if err := h.HandleSettings(ctx, u.Message.From.ID, u.Message.From.Username); err != nil {
					slog.Error("/topics (no arg) failed", slog.Any("err", err))
				}
				return
			}
			// M4 sub-commands are Pro-only: /topics add|remove|list. Each
			// path resolves the user via Registrar (idempotent), checks
			// tier inline, and either delegates to the Handle* method or
			// replies with the upgrade message. The legacy preset toggle
			// — /topics <ID> — stays Free-accessible to preserve the
			// Phase-1 contract.
			tgUserID := u.Message.From.ID
			tgUsername := u.Message.From.Username
			sub := strings.ToLower(parts[1])
			switch sub {
			case "add", "remove":
				if len(parts) < 3 {
					if err := sender.Send(ctx, tgUserID,
						"Использование: /topics "+sub+" <название>"); err != nil {
						slog.Error("/topics usage hint", slog.Any("err", err))
					}
					return
				}
				name := strings.Join(parts[2:], " ")
				gate := func(next func(context.Context, int64, string) error, logTag string) {
					ru, _, err := reg.RegisterOnStart(ctx, tgUserID, tgUsername)
					if err != nil {
						slog.Error(logTag+" resolve user", slog.Any("err", err))
						return
					}
					if tier == nil || !tier.IsPro(ru) {
						if err := sender.Send(ctx, tgUserID,
							"Эта команда доступна в Pro-подписке. Используй /buy"); err != nil {
							slog.Error(logTag+" gate reply", slog.Any("err", err))
						}
						return
					}
					if err := next(ctx, tgUserID, tgUsername); err != nil {
						slog.Error(logTag+" failed", slog.Any("err", err))
					}
				}
				if sub == "add" {
					gate(func(c context.Context, tg int64, un string) error {
						return h.HandleTopicsAdd(c, tg, un, name)
					}, "/topics add")
				} else {
					gate(func(c context.Context, tg int64, un string) error {
						return h.HandleTopicsRemove(c, tg, un, name)
					}, "/topics remove")
				}
			case "list":
				ru, _, err := reg.RegisterOnStart(ctx, tgUserID, tgUsername)
				if err != nil {
					slog.Error("/topics list resolve user", slog.Any("err", err))
					return
				}
				if tier == nil || !tier.IsPro(ru) {
					if err := sender.Send(ctx, tgUserID,
						"Эта команда доступна в Pro-подписке. Используй /buy"); err != nil {
						slog.Error("/topics list gate reply", slog.Any("err", err))
					}
					return
				}
				if err := h.HandleTopicsList(ctx, tgUserID, tgUsername); err != nil {
					slog.Error("/topics list failed", slog.Any("err", err))
				}
			default:
				// Legacy preset toggle: /topics <ID> (e.g. /topics politics).
				if err := h.HandleToggleTopic(ctx, tgUserID, tgUsername, parts[1]); err != nil {
					slog.Error("/topics failed", slog.Any("err", err))
				}
			}
		},
	)
	// /buy is intentionally NOT gated — Free users must reach the upgrade
	// flow. M3 wires the Stars invoice via billing.Service; the handler
	// renders an inline-keyboard button that opens the t.me/$… deeplink.
	b.RegisterHandler(tgbot.HandlerTypeMessageText, "/buy", tgbot.MatchTypePrefix,
		func(ctx context.Context, _ *tgbot.Bot, u *models.Update) {
			if u.Message == nil || u.Message.From == nil {
				return
			}
			if err := h.HandleBuy(ctx, u.Message.From.ID, u.Message.From.Username); err != nil {
				slog.Error("/buy failed", slog.Any("err", err))
			}
		},
	)

	// /weekly is Pro-only. Same gate shape as /alerts: resolve via the
	// Registrar so a brand-new Pro purchaser can hit /weekly right after
	// /start without needing a separate row-creation step.
	b.RegisterHandler(tgbot.HandlerTypeMessageText, "/weekly", tgbot.MatchTypePrefix,
		func(ctx context.Context, _ *tgbot.Bot, u *models.Update) {
			if u.Message == nil || u.Message.From == nil {
				return
			}
			// Reject /weekly_settings here so the prefix matcher does not
			// dispatch the settings command to the weekly handler. The
			// /weekly_settings handler runs immediately below.
			if strings.HasPrefix(u.Message.Text, "/weekly_settings") {
				return
			}
			ru, _, err := reg.RegisterOnStart(ctx, u.Message.From.ID, u.Message.From.Username)
			if err != nil {
				slog.Error("/weekly resolve user", slog.Any("err", err))
				return
			}
			if tier == nil || !tier.IsPro(ru) {
				if err := sender.Send(ctx, u.Message.From.ID,
					"Эта команда доступна в Pro-подписке. Используй /buy"); err != nil {
					slog.Error("/weekly gate reply", slog.Any("err", err))
				}
				return
			}
			if err := h.HandleWeekly(ctx, u.Message.From.ID, u.Message.From.Username); err != nil {
				slog.Error("/weekly failed", slog.Any("err", err))
			}
		},
	)

	// /weekly_settings on|off is Pro-only — Free users cannot enable the
	// weekly cron in the first place. Mirrors the /alerts gate.
	b.RegisterHandler(tgbot.HandlerTypeMessageText, "/weekly_settings", tgbot.MatchTypePrefix,
		func(ctx context.Context, _ *tgbot.Bot, u *models.Update) {
			if u.Message == nil || u.Message.From == nil {
				return
			}
			ru, _, err := reg.RegisterOnStart(ctx, u.Message.From.ID, u.Message.From.Username)
			if err != nil {
				slog.Error("/weekly_settings resolve user", slog.Any("err", err))
				return
			}
			if tier == nil || !tier.IsPro(ru) {
				if err := sender.Send(ctx, u.Message.From.ID,
					"Эта команда доступна в Pro-подписке. Используй /buy"); err != nil {
					slog.Error("/weekly_settings gate reply", slog.Any("err", err))
				}
				return
			}
			parts := strings.Fields(u.Message.Text)
			var args []string
			if len(parts) > 1 {
				args = parts[1:]
			}
			if err := h.HandleWeeklySettings(ctx, u.Message.From.ID, u.Message.From.Username, args); err != nil {
				slog.Error("/weekly_settings failed", slog.Any("err", err))
			}
		},
	)

	// /alerts is Pro-only. Resolve the user via the Registrar (idempotent,
	// returning users get their existing row), check tier, and either
	// dispatch to HandleAlerts or reply with the upgrade message. We
	// inline the gate here instead of reusing RequirePro because /alerts
	// carries variadic args that the CommandHandler signature does not.
	b.RegisterHandler(tgbot.HandlerTypeMessageText, "/alerts", tgbot.MatchTypePrefix,
		func(ctx context.Context, _ *tgbot.Bot, u *models.Update) {
			if u.Message == nil || u.Message.From == nil {
				return
			}
			ru, _, err := reg.RegisterOnStart(ctx, u.Message.From.ID, u.Message.From.Username)
			if err != nil {
				slog.Error("/alerts resolve user", slog.Any("err", err))
				return
			}
			if tier == nil || !tier.IsPro(ru) {
				if err := sender.Send(ctx, u.Message.From.ID,
					"Эта команда доступна в Pro-подписке. Используй /buy"); err != nil {
					slog.Error("/alerts gate reply", slog.Any("err", err))
				}
				return
			}
			parts := strings.Fields(u.Message.Text)
			var args []string
			if len(parts) > 1 {
				args = parts[1:]
			}
			if err := h.HandleAlerts(ctx, u.Message.From.ID, u.Message.From.Username, args); err != nil {
				slog.Error("/alerts failed", slog.Any("err", err))
			}
		},
	)
}

// registerPaymentHandlers attaches the two payment-update routes the bot
// needs once billing is live: pre_checkout_query (we always ACK ok=true)
// and successful_payment (which carries the data billing.Service.Settle
// turns into a Pro grant). The go-telegram/bot library does not provide a
// dedicated update-type constant for these, so we route via the generic
// MatchFunc surface.
func registerPaymentHandlers(b *tgbot.Bot, ph *bot.PaymentHandlers) {
	b.RegisterHandlerMatchFunc(
		func(u *models.Update) bool { return u != nil && u.PreCheckoutQuery != nil },
		func(ctx context.Context, _ *tgbot.Bot, u *models.Update) {
			ph.HandlePreCheckout(ctx, u)
		},
	)
	b.RegisterHandlerMatchFunc(
		func(u *models.Update) bool {
			return u != nil && u.Message != nil && u.Message.SuccessfulPayment != nil
		},
		func(ctx context.Context, _ *tgbot.Bot, u *models.Update) {
			ph.HandleSuccessfulPayment(ctx, u)
		},
	)
}
