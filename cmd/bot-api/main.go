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

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/virsi/mute-bot/internal/bot"
	"github.com/virsi/mute-bot/internal/config"
	"github.com/virsi/mute-bot/internal/digest"
	"github.com/virsi/mute-bot/internal/storage/postgres"
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

	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	rootCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

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

	api, err := bot.NewBotAPI(cfg.BotToken)
	if err != nil {
		return fmt.Errorf("new bot api: %w", err)
	}
	// Sender wraps BotAPI with per-chat rate limiting for digest pushes.
	// Slash-command replies bypass it — they go straight through BotAPI's
	// API.Send because user-facing latency matters more than back-pressure
	// at one message per command invocation.
	sender := bot.NewSender(bot.SenderDeps{API: api, PerChatPerSec: 1})

	users := postgres.NewUsersRepo(pool)
	settings := postgres.NewSettingsRepo(pool)
	clusters := postgres.NewClustersRepo(pool)
	deliveries := postgres.NewDeliveriesRepo(pool)
	channelsRepo := postgres.NewChannelsRepo(pool)

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
		Users:     users,
		Settings:  settings,
		Assembler: asmAdapter,
		API:       api,
	})

	registerHandlers(api.Bot(), h)

	slog.Info("bot-api: starting long poll")
	api.Bot().Start(rootCtx)
	slog.Info("bot-api: stopped")
	return nil
}

// registerHandlers attaches one prefix handler per slash command. The
// go-telegram/bot router dispatches by prefix-match on Message.Text, so the
// handlers themselves are responsible for parsing trailing arguments.
//
// Errors returned from h.Handle* are logged and swallowed: a single failing
// command should not stop the long-poll loop.
func registerHandlers(b *tgbot.Bot, h *bot.Handlers) {
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
}
