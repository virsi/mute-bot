package bot

import (
	"context"
	"encoding/json"
	"log/slog"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// Settler is the surface PaymentHandlers calls into on successful_payment.
// Satisfied by *billing.Service in production. The provider arg keys
// dispatch to the right Provider instance — PaymentHandlers always
// passes "tg_stars" because Telegram successful_payment updates can only
// originate from the Stars channel in this bot.
type Settler interface {
	Settle(ctx context.Context, provider string, raw []byte) (bool, error)
}

// PreCheckoutAcker abstracts the Bot API call that answers a
// pre_checkout_query. Defined as an interface so unit tests do not need
// to dial Telegram. Satisfied by *tgbot.Bot.
type PreCheckoutAcker interface {
	AnswerPreCheckoutQuery(ctx context.Context, params *tgbot.AnswerPreCheckoutQueryParams) (bool, error)
}

// PaymentHandlersDeps configures NewPaymentHandlers.
type PaymentHandlersDeps struct {
	// Acker is the Bot API client used to answer pre_checkout_query.
	Acker PreCheckoutAcker
	// Settler is the billing orchestrator that processes successful_payment.
	Settler Settler
	// API is used to send the user-facing confirmation reply after a
	// successful payment. Use SendOnly or Client.
	API SendAPI
	// Logger overrides slog.Default. Optional.
	Logger *slog.Logger
}

// PaymentHandlers wires Telegram payment updates (pre_checkout_query and
// successful_payment) onto the billing orchestrator. The bot's long-polling
// loop is responsible for routing updates here via RegisterHandlerMatchFunc.
type PaymentHandlers struct {
	acker   PreCheckoutAcker
	settler Settler
	api     SendAPI
	logger  *slog.Logger
}

// NewPaymentHandlers constructs PaymentHandlers. acker, settler, api are
// mandatory.
func NewPaymentHandlers(d PaymentHandlersDeps) *PaymentHandlers {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	return &PaymentHandlers{
		acker:   d.Acker,
		settler: d.Settler,
		api:     d.API,
		logger:  d.Logger,
	}
}

// HandlePreCheckout always answers OK to the pre_checkout_query. Telegram
// requires a reply within 10 seconds, so we acknowledge unconditionally —
// amount and currency were already pinned to 99 XTR at createInvoiceLink
// time, and there is no per-user inventory to reserve.
func (p *PaymentHandlers) HandlePreCheckout(ctx context.Context, u *models.Update) {
	if u == nil || u.PreCheckoutQuery == nil {
		return
	}
	if _, err := p.acker.AnswerPreCheckoutQuery(ctx, &tgbot.AnswerPreCheckoutQueryParams{
		PreCheckoutQueryID: u.PreCheckoutQuery.ID,
		OK:                 true,
	}); err != nil {
		p.logger.ErrorContext(ctx, "answer pre_checkout_query",
			slog.String("query_id", u.PreCheckoutQuery.ID),
			slog.Any("err", err),
		)
	}
}

// HandleSuccessfulPayment marshals the embedded SuccessfulPayment plus the
// from-user id into a JSON blob the billing.Service can parse, then
// delegates to Settler. The reply text differs depending on whether the
// orchestrator actually granted Pro (false → duplicate webhook).
func (p *PaymentHandlers) HandleSuccessfulPayment(ctx context.Context, u *models.Update) {
	if u == nil || u.Message == nil || u.Message.SuccessfulPayment == nil || u.Message.From == nil {
		return
	}
	sp := u.Message.SuccessfulPayment
	raw, err := json.Marshal(map[string]any{
		"user_id":                    u.Message.From.ID,
		"currency":                   sp.Currency,
		"total_amount":               sp.TotalAmount,
		"invoice_payload":            sp.InvoicePayload,
		"provider_payment_charge_id": sp.ProviderPaymentChargeID,
		"telegram_payment_charge_id": sp.TelegramPaymentChargeID,
	})
	if err != nil {
		p.logger.ErrorContext(ctx, "marshal successful_payment",
			slog.Int64("tg_user_id", u.Message.From.ID),
			slog.Any("err", err),
		)
		if sendErr := p.api.Send(ctx, u.Message.From.ID,
			"Платёж получен, но активация подписки не завершилась. Напишите в поддержку."); sendErr != nil {
			p.logger.ErrorContext(ctx, "send fail-reply", slog.Any("err", sendErr))
		}
		return
	}
	granted, err := p.settler.Settle(ctx, "tg_stars", raw)
	if err != nil {
		p.logger.ErrorContext(ctx, "settle payment",
			slog.Int64("tg_user_id", u.Message.From.ID),
			slog.Any("err", err),
		)
		if sendErr := p.api.Send(ctx, u.Message.From.ID,
			"Платёж получен, но активация подписки не завершилась. Напишите в поддержку."); sendErr != nil {
			p.logger.ErrorContext(ctx, "send fail-reply", slog.Any("err", sendErr))
		}
		return
	}
	msg := "Оплата получена, Pro активирован на 30 дней."
	if !granted {
		msg = "Этот платёж уже был обработан ранее. Подписка действует — спасибо!"
	}
	if err := p.api.Send(ctx, u.Message.From.ID, msg); err != nil {
		p.logger.ErrorContext(ctx, "send confirmation",
			slog.Int64("tg_user_id", u.Message.From.ID),
			slog.Any("err", err),
		)
	}
}
