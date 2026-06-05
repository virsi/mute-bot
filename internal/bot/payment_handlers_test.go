package bot

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/stretchr/testify/require"
)

// fakeAcker captures pre_checkout_query answers.
type fakeAcker struct {
	calls []*tgbot.AnswerPreCheckoutQueryParams
	err   error
}

func (f *fakeAcker) AnswerPreCheckoutQuery(_ context.Context, p *tgbot.AnswerPreCheckoutQueryParams) (bool, error) {
	f.calls = append(f.calls, p)
	if f.err != nil {
		return false, f.err
	}
	return true, nil
}

// fakeSettler records raw Settle payloads and lets each test pin granted/err.
type fakeSettler struct {
	granted bool
	err     error
	calls   [][]byte
}

func (f *fakeSettler) Settle(_ context.Context, raw []byte) (bool, error) {
	f.calls = append(f.calls, raw)
	return f.granted, f.err
}

func newSuccessfulPaymentUpdate(t *testing.T) *models.Update {
	t.Helper()
	return &models.Update{
		Message: &models.Message{
			From: &models.User{ID: 555},
			SuccessfulPayment: &models.SuccessfulPayment{
				Currency:                "XTR",
				TotalAmount:             99,
				InvoicePayload:          "pro_30d:555",
				ProviderPaymentChargeID: "prov-1",
				TelegramPaymentChargeID: "tg-1",
			},
		},
	}
}

func TestPaymentHandlers_HandlePreCheckout_AlwaysAcksOK(t *testing.T) {
	acker := &fakeAcker{}
	ph := NewPaymentHandlers(PaymentHandlersDeps{Acker: acker, Settler: &fakeSettler{}, API: &capturedSender{}})

	ph.HandlePreCheckout(context.Background(), &models.Update{
		PreCheckoutQuery: &models.PreCheckoutQuery{ID: "q-1", TotalAmount: 99},
	})
	require.Len(t, acker.calls, 1)
	require.True(t, acker.calls[0].OK)
	require.Equal(t, "q-1", acker.calls[0].PreCheckoutQueryID)
}

func TestPaymentHandlers_HandlePreCheckout_NoQueryIsNoop(t *testing.T) {
	acker := &fakeAcker{}
	ph := NewPaymentHandlers(PaymentHandlersDeps{Acker: acker, Settler: &fakeSettler{}, API: &capturedSender{}})
	ph.HandlePreCheckout(context.Background(), &models.Update{})
	ph.HandlePreCheckout(context.Background(), nil)
	require.Empty(t, acker.calls)
}

func TestPaymentHandlers_HandleSuccessfulPayment_FirstHookSendsActivatedMsg(t *testing.T) {
	send := &capturedSender{}
	settler := &fakeSettler{granted: true}
	ph := NewPaymentHandlers(PaymentHandlersDeps{Acker: &fakeAcker{}, Settler: settler, API: send})

	ph.HandleSuccessfulPayment(context.Background(), newSuccessfulPaymentUpdate(t))

	require.Len(t, settler.calls, 1)
	require.Len(t, send.msgs, 1)
	require.Contains(t, send.msgs[0], "Pro активирован")

	var got map[string]any
	require.NoError(t, json.Unmarshal(settler.calls[0], &got))
	require.Equal(t, "XTR", got["currency"])
	require.EqualValues(t, 99, got["total_amount"])
	require.Equal(t, "pro_30d:555", got["invoice_payload"])
	require.Equal(t, "prov-1", got["provider_payment_charge_id"])
}

func TestPaymentHandlers_HandleSuccessfulPayment_DuplicateSendsAlreadyProcessed(t *testing.T) {
	send := &capturedSender{}
	settler := &fakeSettler{granted: false}
	ph := NewPaymentHandlers(PaymentHandlersDeps{Acker: &fakeAcker{}, Settler: settler, API: send})

	ph.HandleSuccessfulPayment(context.Background(), newSuccessfulPaymentUpdate(t))
	require.Len(t, send.msgs, 1)
	require.Contains(t, send.msgs[0], "уже был обработан")
}

func TestPaymentHandlers_HandleSuccessfulPayment_SettleErrorSendsSupportMsg(t *testing.T) {
	send := &capturedSender{}
	settler := &fakeSettler{err: errors.New("db down")}
	ph := NewPaymentHandlers(PaymentHandlersDeps{Acker: &fakeAcker{}, Settler: settler, API: send})

	ph.HandleSuccessfulPayment(context.Background(), newSuccessfulPaymentUpdate(t))
	require.Len(t, send.msgs, 1)
	require.Contains(t, send.msgs[0], "поддержку")
}

func TestPaymentHandlers_HandleSuccessfulPayment_NoSuccessfulPaymentIsNoop(t *testing.T) {
	send := &capturedSender{}
	settler := &fakeSettler{}
	ph := NewPaymentHandlers(PaymentHandlersDeps{Acker: &fakeAcker{}, Settler: settler, API: send})

	ph.HandleSuccessfulPayment(context.Background(), &models.Update{})
	ph.HandleSuccessfulPayment(context.Background(), &models.Update{Message: &models.Message{}})
	ph.HandleSuccessfulPayment(context.Background(), nil)

	require.Empty(t, settler.calls)
	require.Empty(t, send.msgs)
}
