package bot

import (
	"context"
	"fmt"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// BotAPI is the concrete Bot API implementation backed by go-telegram/bot.
// It implements the API interface that Sender calls through.
//
//nolint:revive // BotAPI is descriptive in the bot package context.
type BotAPI struct {
	b *tgbot.Bot
}

// NewBotAPI constructs a BotAPI from a bot token. The underlying bot is not
// started here — long-polling/update dispatch is M11's concern; this type is
// just a thin Send adapter for the digest path.
func NewBotAPI(token string) (*BotAPI, error) {
	b, err := tgbot.New(token)
	if err != nil {
		return nil, fmt.Errorf("new bot: %w", err)
	}
	return &BotAPI{b: b}, nil
}

// Send implements API: it pushes a single HTML-parsed message to chatID.
// Errors from the Telegram API are returned verbatim — Sender does not
// distinguish between them yet.
func (a *BotAPI) Send(ctx context.Context, chatID int64, text string) error {
	_, err := a.b.SendMessage(ctx, &tgbot.SendMessageParams{
		ChatID:    chatID,
		Text:      text,
		ParseMode: models.ParseModeHTML,
	})
	if err != nil {
		return fmt.Errorf("send message: %w", err)
	}
	return nil
}

// Bot returns the underlying go-telegram/bot client for callers that need to
// register handlers or run the long-polling loop (M11).
func (a *BotAPI) Bot() *tgbot.Bot { return a.b }
