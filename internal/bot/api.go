package bot

import (
	"context"
	"fmt"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// SendOnly is the narrow Bot API client used by cmd/processor's digest
// path. Provides exactly one method — Send — and intentionally hides the
// long-polling surface so the processor cannot accidentally call
// getUpdates and contend with cmd/bot-api.
type SendOnly struct {
	b *tgbot.Bot
}

// NewSendOnly constructs a SendOnly. It dials the Bot API to validate the
// token; no long-polling loop is started.
func NewSendOnly(token string) (*SendOnly, error) {
	b, err := tgbot.New(token)
	if err != nil {
		return nil, fmt.Errorf("new bot: %w", err)
	}
	return &SendOnly{b: b}, nil
}

// Send pushes a single HTML-parsed message to chatID. Implements API.
func (s *SendOnly) Send(ctx context.Context, chatID int64, text string) error {
	_, err := s.b.SendMessage(ctx, &tgbot.SendMessageParams{
		ChatID:    chatID,
		Text:      text,
		ParseMode: models.ParseModeHTML,
	})
	if err != nil {
		return fmt.Errorf("send message: %w", err)
	}
	return nil
}

// SendURLButton pushes an HTML-parsed message with a single inline-keyboard
// button that opens the supplied URL. Used by /buy to deliver the Stars
// invoice link with a one-tap CTA. buttonText is the rendered label;
// chatID is the destination chat.
func (s *SendOnly) SendURLButton(ctx context.Context, chatID int64, text, buttonText, url string) error {
	_, err := s.b.SendMessage(ctx, &tgbot.SendMessageParams{
		ChatID:    chatID,
		Text:      text,
		ParseMode: models.ParseModeHTML,
		ReplyMarkup: &models.InlineKeyboardMarkup{
			InlineKeyboard: [][]models.InlineKeyboardButton{
				{{Text: buttonText, URL: url}},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("send url-button message: %w", err)
	}
	return nil
}

// SendTwoURLButtons pushes one HTML-parsed message with two inline-keyboard
// URL buttons stacked vertically. Used by /buy to offer the user a choice
// between Telegram Stars and YooKassa payment channels.
func (s *SendOnly) SendTwoURLButtons(ctx context.Context, chatID int64, text,
	label1, url1, label2, url2 string,
) error {
	_, err := s.b.SendMessage(ctx, &tgbot.SendMessageParams{
		ChatID:    chatID,
		Text:      text,
		ParseMode: models.ParseModeHTML,
		ReplyMarkup: &models.InlineKeyboardMarkup{
			InlineKeyboard: [][]models.InlineKeyboardButton{
				{{Text: label1, URL: url1}},
				{{Text: label2, URL: url2}},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("send two-url-button message: %w", err)
	}
	return nil
}

// Client is the full Bot API used by cmd/bot-api for long-polling, command
// handlers, sendInvoice, and pre-checkout webhooks. Embeds SendOnly so it
// satisfies the API interface used by Sender.
type Client struct {
	SendOnly
}

// NewClient constructs a Client.
func NewClient(token string) (*Client, error) {
	s, err := NewSendOnly(token)
	if err != nil {
		return nil, err
	}
	return &Client{SendOnly: *s}, nil
}

// Bot returns the underlying go-telegram/bot client so cmd/bot-api can
// register handlers and run long polling.
func (c *Client) Bot() *tgbot.Bot { return c.b }
