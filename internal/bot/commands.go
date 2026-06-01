package bot

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/virsi/mute-bot/internal/storage/postgres"
)

// UsersRepo is the slice of postgres.UsersRepo the command handlers need.
// Kept narrow so unit tests can substitute a fake.
type UsersRepo interface {
	GetOrCreate(ctx context.Context, tgUserID int64, username string) (postgres.User, bool, error)
}

// SettingsRepo is the slice of postgres.SettingsRepo the command handlers
// need. Get and Upsert are sufficient for the read-modify-write flow used
// by /threshold and /topics.
type SettingsRepo interface {
	Get(ctx context.Context, userID int64) (postgres.Settings, error)
	Upsert(ctx context.Context, userID int64, in postgres.SettingsUpdate) error
}

// AssembleReq is the parameter object handed to the assembler from the
// /digest handler. Mirrors digest.AssembleRequest but lives in the bot
// package so handlers do not import digest directly.
type AssembleReq struct {
	UserID   int64
	TGUserID int64
	Channel  string
	Title    string
}

// AssemblerIface is the slice of *digest.Assembler the handlers need. The
// wiring layer (cmd/bot-api) bridges digest.AssembleRequest ↔ AssembleReq
// via AssemblerFunc below.
type AssemblerIface interface {
	Assemble(ctx context.Context, req AssembleReq) error
}

// AssemblerFunc adapts a function to AssemblerIface so cmd/bot-api can
// supply an inline closure that translates AssembleReq into the digest
// package's own request type.
type AssemblerFunc func(ctx context.Context, r AssembleReq) error

// Assemble calls f.
func (f AssemblerFunc) Assemble(ctx context.Context, r AssembleReq) error { return f(ctx, r) }

// Compile-time guarantee: the func adapter satisfies the interface.
var _ AssemblerIface = AssemblerFunc(nil)

// SendAPI is the minimal contract command handlers need to push plain-text
// replies. Satisfied by *BotAPI in production.
type SendAPI interface {
	Send(ctx context.Context, chatID int64, text string) error
}

// HandlersDeps groups the handler collaborators. Assembler is optional —
// only /digest needs it, so a process that never runs /digest can leave
// it nil.
type HandlersDeps struct {
	Users     UsersRepo
	Settings  SettingsRepo
	Assembler AssemblerIface
	API       SendAPI
}

// Handlers groups the per-command methods. One method per slash command;
// the wiring layer is responsible for mapping go-telegram/bot's update
// callbacks onto these methods.
type Handlers struct{ d HandlersDeps }

// NewHandlers constructs a Handlers bound to d. The caller is responsible
// for providing all collaborators (no defaults).
func NewHandlers(d HandlersDeps) *Handlers { return &Handlers{d: d} }

// welcome is the message shown to first-time users on /start. Kept as a
// package var so tests can inspect it via reflection if needed.
const welcome = `Привет!

Я бот-агрегатор новостей. Читаю TG-каналы, схлопываю дубли, ранжирую по важности и присылаю компактные сводки.

/digest — сводка прямо сейчас
/topics ID — включить/выключить тему
/threshold N — порог важности 0..100
/settings — текущие настройки

По умолчанию: темы [politics, it], порог 50, расписание 08:00 и 19:00 (MSK).`

// HandleStart implements /start. It upserts the user row and — on a fresh
// creation — seeds default settings (topics [politics, it], threshold 50,
// schedule 08:00/19:00 MSK). Returning users get only the welcome message;
// their existing settings are preserved.
func (h *Handlers) HandleStart(ctx context.Context, tgUserID int64, username string) error {
	u, created, err := h.d.Users.GetOrCreate(ctx, tgUserID, username)
	if err != nil {
		return fmt.Errorf("get_or_create user: %w", err)
	}
	if created {
		sched, _ := json.Marshal(map[string]any{
			"times": []string{"08:00", "19:00"},
			"tz":    "Europe/Moscow",
		})
		if err := h.d.Settings.Upsert(ctx, u.ID, postgres.SettingsUpdate{
			Topics:       []string{"politics", "it"},
			Threshold:    50,
			ScheduleJSON: sched,
		}); err != nil {
			return fmt.Errorf("seed settings: %w", err)
		}
	}
	return h.d.API.Send(ctx, tgUserID, welcome)
}

// HandleDigest implements /digest. It looks up the user (creating one if
// this is their first interaction) and delegates to the assembler with
// the on-demand channel tag.
func (h *Handlers) HandleDigest(ctx context.Context, tgUserID int64, username string) error {
	u, _, err := h.d.Users.GetOrCreate(ctx, tgUserID, username)
	if err != nil {
		return fmt.Errorf("get_or_create user: %w", err)
	}
	return h.d.Assembler.Assemble(ctx, AssembleReq{
		UserID:   u.ID,
		TGUserID: tgUserID,
		Channel:  "on_demand",
		Title:    "Сводка",
	})
}

// HandleThreshold implements /threshold N. The threshold is a 0..100
// integer; out-of-range values trigger a friendly error message and the
// settings row is left untouched.
func (h *Handlers) HandleThreshold(ctx context.Context, tgUserID int64, username string, threshold int) error {
	if threshold < 0 || threshold > 100 {
		return h.d.API.Send(ctx, tgUserID, "Порог должен быть от 0 до 100.")
	}
	u, _, err := h.d.Users.GetOrCreate(ctx, tgUserID, username)
	if err != nil {
		return fmt.Errorf("get_or_create user: %w", err)
	}
	s, err := h.d.Settings.Get(ctx, u.ID)
	if err != nil {
		return fmt.Errorf("get settings: %w", err)
	}
	if err := h.d.Settings.Upsert(ctx, u.ID, postgres.SettingsUpdate{
		Topics:         s.Topics,
		Threshold:      threshold,
		ScheduleJSON:   s.ScheduleJSON,
		AlertsEnabled:  s.AlertsEnabled,
		AlertThreshold: s.AlertThreshold,
		WeeklyEnabled:  s.WeeklyEnabled,
	}); err != nil {
		return fmt.Errorf("upsert settings: %w", err)
	}
	return h.d.API.Send(ctx, tgUserID, fmt.Sprintf("Порог важности обновлён: %d.", threshold))
}

// HandleToggleTopic implements /topics ID. If topicID is currently in the
// user's topic list it is removed; otherwise it is appended. The reply
// echoes the resulting state so the user sees what changed.
func (h *Handlers) HandleToggleTopic(ctx context.Context, tgUserID int64, username string, topicID string) error {
	u, _, err := h.d.Users.GetOrCreate(ctx, tgUserID, username)
	if err != nil {
		return fmt.Errorf("get_or_create user: %w", err)
	}
	s, err := h.d.Settings.Get(ctx, u.ID)
	if err != nil {
		return fmt.Errorf("get settings: %w", err)
	}
	wasOn := false
	updated := make([]string, 0, len(s.Topics)+1)
	for _, t := range s.Topics {
		if t == topicID {
			wasOn = true
			continue
		}
		updated = append(updated, t)
	}
	if !wasOn {
		updated = append(updated, topicID)
	}
	if err := h.d.Settings.Upsert(ctx, u.ID, postgres.SettingsUpdate{
		Topics:         updated,
		Threshold:      s.Threshold,
		ScheduleJSON:   s.ScheduleJSON,
		AlertsEnabled:  s.AlertsEnabled,
		AlertThreshold: s.AlertThreshold,
		WeeklyEnabled:  s.WeeklyEnabled,
	}); err != nil {
		return fmt.Errorf("upsert settings: %w", err)
	}
	state := "включена"
	if wasOn {
		state = "выключена"
	}
	return h.d.API.Send(ctx, tgUserID, fmt.Sprintf("Тема %s: %s", topicID, state))
}

// HandleSettings implements /settings. Renders the current row as a
// plain-text message. The schedule JSON is shown verbatim — Phase-1 users
// edit it via /threshold and /topics, so seeing the raw JSON is acceptable.
func (h *Handlers) HandleSettings(ctx context.Context, tgUserID int64, username string) error {
	u, _, err := h.d.Users.GetOrCreate(ctx, tgUserID, username)
	if err != nil {
		return fmt.Errorf("get_or_create user: %w", err)
	}
	s, err := h.d.Settings.Get(ctx, u.ID)
	if err != nil {
		return fmt.Errorf("get settings: %w", err)
	}
	text := fmt.Sprintf(
		"Настройки:\n\nТемы: %v\nПорог: %d\nРасписание: %s\nТариф: %s",
		s.Topics, s.Threshold, string(s.ScheduleJSON), u.Tier,
	)
	return h.d.API.Send(ctx, tgUserID, text)
}
