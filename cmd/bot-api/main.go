// Command bot-api is the Telegram Bot API process. It registers handlers
// for the slash commands defined in internal/bot/commands.go and runs the
// go-telegram/bot long-polling loop. The /digest command delegates to a
// digest.Assembler so on-demand digests share the same pipeline as the
// scheduled deliveries produced by cmd/scheduler.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/virsi/mute-bot/internal/billing"
	"github.com/virsi/mute-bot/internal/bot"
	"github.com/virsi/mute-bot/internal/config"
	"github.com/virsi/mute-bot/internal/digest"
	"github.com/virsi/mute-bot/internal/obs"
	"github.com/virsi/mute-bot/internal/storage/postgres"
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

	// Metrics endpoint on the bot-api slot. Exposes long-poll loop health
	// and Bot API send counters scraped by Prometheus.
	metricsSrv := obs.ServeMetrics(":9103")
	defer func() { _ = metricsSrv.Close() }()

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

	// usersSvc unifies /start, billing webhooks (M3) and the expiry sweep
	// behind one service so the upsert + default-seed flow stays in one
	// place. It also doubles as the TierChecker for /settings and the
	// Pro-gate middleware below.
	usersSvc := users.NewService(users.Deps{Users: usersRepo, Settings: settings})

	// Billing wiring: StarsProvider over the live Bot API + Service over
	// (provider, subsRepo, usersSvc). Service.Settle is the idempotent
	// activation entry point invoked by the payment_handlers below.
	starsProvider := billing.NewStarsProvider(client.Bot())
	billingSvc := billing.NewService(billing.Deps{
		Provider: starsProvider,
		Subs:     subsRepo,
		Users:    usersSvc,
	})
	paymentHandlers := bot.NewPaymentHandlers(bot.PaymentHandlersDeps{
		Acker:   client.Bot(),
		Settler: billingSvc,
		API:     &client.SendOnly,
	})

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

	h := bot.NewHandlers(bot.HandlersDeps{
		Users:     usersRepo,
		Settings:  settings,
		Assembler: asmAdapter,
		API:       &client.SendOnly,
		Registrar: usersSvc,
		Tier:      usersSvc,
		Invoicer:  billingSvc,
		ButtonAPI: &client.SendOnly,
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
			if err := h.HandleToggleTopic(ctx, u.Message.From.ID, u.Message.From.Username, parts[1]); err != nil {
				slog.Error("/topics failed", slog.Any("err", err))
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
