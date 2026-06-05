package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/virsi/mute-bot/internal/storage/postgres"
	"github.com/virsi/mute-bot/internal/topics"
)

// defaultScheduleTZ is the timezone applied when /schedule is invoked
// without an explicit tz argument. Phase-1 ships RU-only, so Moscow is
// the right default for the vast majority of users.
const defaultScheduleTZ = "Europe/Moscow"

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

// WeeklyAssemblerIface is the slice of digest.WeeklyAssembler the /weekly
// handler needs. cmd/bot-api wires *digest.WeeklyAssembler directly via
// WeeklyAssemblerFunc so the bot package keeps the WeeklyRequest type out
// of its imports.
type WeeklyAssemblerIface interface {
	BuildWeekly(ctx context.Context, userID, tgUserID int64) error
}

// WeeklyAssemblerFunc adapts a closure to WeeklyAssemblerIface.
type WeeklyAssemblerFunc func(ctx context.Context, userID, tgUserID int64) error

// BuildWeekly calls f.
func (f WeeklyAssemblerFunc) BuildWeekly(ctx context.Context, u, tg int64) error {
	return f(ctx, u, tg)
}

// Compile-time guarantee the func adapter satisfies the interface.
var _ WeeklyAssemblerIface = WeeklyAssemblerFunc(nil)

// AssemblerFunc adapts a function to AssemblerIface so cmd/bot-api can
// supply an inline closure that translates AssembleReq into the digest
// package's own request type.
type AssemblerFunc func(ctx context.Context, r AssembleReq) error

// Assemble calls f.
func (f AssemblerFunc) Assemble(ctx context.Context, r AssembleReq) error { return f(ctx, r) }

// Compile-time guarantee: the func adapter satisfies the interface.
var _ AssemblerIface = AssemblerFunc(nil)

// SendAPI is the minimal contract command handlers need to push plain-text
// replies. Satisfied by *Client (via embedded SendOnly) in production.
type SendAPI interface {
	Send(ctx context.Context, chatID int64, text string) error
}

// URLButtonSender is the optional surface used by /buy to push a message
// with a single inline-keyboard button that opens the Stars invoice URL.
// Implemented by *SendOnly. Kept separate from SendAPI so unit tests that
// only need Send can keep stubbing the smaller interface.
type URLButtonSender interface {
	SendURLButton(ctx context.Context, chatID int64, text, buttonText, url string) error
}

// Invoicer is the surface HandleBuy calls into. Satisfied by
// *billing.Service in production.
type Invoicer interface {
	CreateInvoice(ctx context.Context, tgUserID int64, plan string) (string, error)
}

// Registrar is the slice of users.Service used on /start. Wrapping the
// upsert + seed-defaults flow behind a single call keeps the handler
// free of seeding logic that needs to stay in sync with billing flows.
type Registrar interface {
	RegisterOnStart(ctx context.Context, tgUserID int64, username string) (postgres.User, bool, error)
}

// TierChecker is the slice of users.Service used by /settings and the
// Pro-gate middleware. Kept as a small interface so unit tests can stub
// it without instantiating the full users.Service.
type TierChecker interface {
	IsPro(u postgres.User) bool
}

// TopicsService is the slice of topics.Service the /topics handlers
// need. Kept narrow so unit tests can stub the entire service without
// pulling in the embeddings dependency.
//
// AddTopic/RemoveTopic/ListTopics are the M4 Pro-only command surface;
// the legacy /topics ID preset toggle continues to live in
// HandleToggleTopic and is not part of this interface.
type TopicsService interface {
	AddTopic(ctx context.Context, userID int64, name string) error
	RemoveTopic(ctx context.Context, userID int64, name string) error
	ListTopics(ctx context.Context, userID int64) ([]string, error)
}

// HandlersDeps groups the handler collaborators. Assembler, Registrar
// and Tier are optional in unit tests; production wiring must provide
// all three so /start and /settings behave correctly. Invoicer and
// ButtonAPI together unlock the real /buy flow — when either is nil the
// command falls back to a stub message (preserved for the M2 milestone).
// Topics is the M4 hook for the Pro-only custom-topic subsystem; when
// nil the /topics add|remove|list handlers reply with a friendly stub.
type HandlersDeps struct {
	Users     UsersRepo
	Settings  SettingsRepo
	Assembler AssemblerIface
	API       SendAPI
	Registrar Registrar
	Tier      TierChecker
	Invoicer  Invoicer
	ButtonAPI URLButtonSender
	Topics    TopicsService
	// Weekly is the on-demand weekly digest assembler used by /weekly.
	// Nil-safe — when unwired the handler falls back to a friendly stub
	// message so unit tests can exercise the file without M1 plumbing.
	Weekly WeeklyAssemblerIface
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
/schedule HH:MM,HH:MM [TZ] — время доставки сводки
/settings — текущие настройки

По умолчанию: темы [politics, it], порог 50, расписание 08:00 и 19:00 (MSK).`

// HandleStart implements /start. It delegates the upsert + first-touch
// seeding to the Registrar (a users.Service) so the same flow drives
// /start, billing webhooks, and any future entry-points. When no
// Registrar is wired (unit tests), it falls back to the legacy inline
// upsert + seed so existing tests stay valid.
func (h *Handlers) HandleStart(ctx context.Context, tgUserID int64, username string) error {
	if h.d.Registrar != nil {
		if _, _, err := h.d.Registrar.RegisterOnStart(ctx, tgUserID, username); err != nil {
			return fmt.Errorf("register: %w", err)
		}
		return h.d.API.Send(ctx, tgUserID, welcome)
	}
	u, created, err := h.d.Users.GetOrCreate(ctx, tgUserID, username)
	if err != nil {
		return fmt.Errorf("get_or_create user: %w", err)
	}
	if created {
		sched, err := json.Marshal(map[string]any{
			"times": []string{"08:00", "19:00"},
			"tz":    "Europe/Moscow",
		})
		if err != nil {
			return fmt.Errorf("marshal default schedule: %w", err)
		}
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

// HandleSchedule implements /schedule TIMES [TZ]. TIMES is a comma-separated
// list of HH:MM values; TZ is an IANA zone identifier (defaults to
// Europe/Moscow when empty). Every time is validated strictly via
// time.Parse("15:04", t) — if any entry is malformed the row is left
// untouched and the user gets a usage hint. The resulting JSON has the
// same shape the scheduler expects: {"times":[...],"tz":"..."}.
func (h *Handlers) HandleSchedule(ctx context.Context, tgUserID int64, username, timesCSV, tz string) error {
	if strings.TrimSpace(timesCSV) == "" {
		return h.d.API.Send(ctx, tgUserID,
			"Использование: /schedule HH:MM,HH:MM [TZ]\nПример: /schedule 08:00,19:00 Europe/Moscow")
	}
	if strings.TrimSpace(tz) == "" {
		tz = defaultScheduleTZ
	}

	raw := strings.Split(timesCSV, ",")
	times := make([]string, 0, len(raw))
	for _, t := range raw {
		t = strings.TrimSpace(t)
		if _, err := time.Parse("15:04", t); err != nil {
			return h.d.API.Send(ctx, tgUserID,
				fmt.Sprintf("Время %q должно быть в формате HH:MM (например 08:00).", t))
		}
		times = append(times, t)
	}

	u, _, err := h.d.Users.GetOrCreate(ctx, tgUserID, username)
	if err != nil {
		return fmt.Errorf("get_or_create user: %w", err)
	}
	s, err := h.d.Settings.Get(ctx, u.ID)
	if err != nil {
		return fmt.Errorf("get settings: %w", err)
	}

	sched, err := json.Marshal(map[string]any{"times": times, "tz": tz})
	if err != nil {
		return fmt.Errorf("marshal schedule: %w", err)
	}

	if err := h.d.Settings.Upsert(ctx, u.ID, postgres.SettingsUpdate{
		Topics:         s.Topics,
		Threshold:      s.Threshold,
		ScheduleJSON:   sched,
		AlertsEnabled:  s.AlertsEnabled,
		AlertThreshold: s.AlertThreshold,
		WeeklyEnabled:  s.WeeklyEnabled,
	}); err != nil {
		return fmt.Errorf("upsert settings: %w", err)
	}

	return h.d.API.Send(ctx, tgUserID,
		fmt.Sprintf("Расписание обновлено: %s (%s).", strings.Join(times, ", "), tz))
}

// HandleSettings implements /settings. Renders the current row as a
// plain-text message. The schedule JSON is shown verbatim — Phase-1 users
// edit it via /threshold and /topics, so seeing the raw JSON is acceptable.
// The tier line shows the active plan and, for Pro users, the deadline.
func (h *Handlers) HandleSettings(ctx context.Context, tgUserID int64, username string) error {
	u, _, err := h.d.Users.GetOrCreate(ctx, tgUserID, username)
	if err != nil {
		return fmt.Errorf("get_or_create user: %w", err)
	}
	s, err := h.d.Settings.Get(ctx, u.ID)
	if err != nil {
		return fmt.Errorf("get settings: %w", err)
	}
	tierLine := fmt.Sprintf("Тариф: %s", u.Tier)
	if u.Tier == "pro" && u.TierUntil != nil {
		tierLine += fmt.Sprintf(" (до %s)", u.TierUntil.Format("02 Jan 2006"))
	}
	weeklyState := "выкл"
	if s.WeeklyEnabled {
		weeklyState = "вкл"
	}
	text := fmt.Sprintf(
		"Настройки:\n\nТемы: %v\nПорог: %d\nРасписание: %s\nЕженедельный дайджест: %s\n%s",
		s.Topics, s.Threshold, string(s.ScheduleJSON), weeklyState, tierLine,
	)
	return h.d.API.Send(ctx, tgUserID, text)
}

// HandleWeekly implements the on-demand /weekly command. Pro-only — the
// gate is applied by the wiring layer (RequirePro middleware). On-demand
// /weekly intentionally bypasses the once-per-ISO-week anti-repeat so a
// Pro user can re-pull the digest on demand; the assembler is configured
// with SkipAntiRepeat=true in cmd/bot-api so this handler does not need
// any special-casing.
func (h *Handlers) HandleWeekly(ctx context.Context, tgUserID int64, username string) error {
	if h.d.Weekly == nil {
		return h.d.API.Send(ctx, tgUserID, "Недельная сводка скоро будет доступна.")
	}
	u, _, err := h.d.Users.GetOrCreate(ctx, tgUserID, username)
	if err != nil {
		return fmt.Errorf("get_or_create user: %w", err)
	}
	if err := h.d.Weekly.BuildWeekly(ctx, u.ID, tgUserID); err != nil {
		return fmt.Errorf("build weekly: %w", err)
	}
	return nil
}

// HandleWeeklySettings implements /weekly_settings on|off. Pro-only via
// the wiring layer. Toggles user_settings.weekly_enabled; the scheduler
// loader picks up the change on its next reload tick so the cron starts
// or stops emitting Sunday-18:00 jobs for the user without any process
// restart.
//
// No argument → echo the current state. Unknown argument → usage hint.
func (h *Handlers) HandleWeeklySettings(ctx context.Context, tgUserID int64, username string, args []string) error {
	u, _, err := h.d.Users.GetOrCreate(ctx, tgUserID, username)
	if err != nil {
		return fmt.Errorf("get_or_create user: %w", err)
	}
	s, err := h.d.Settings.Get(ctx, u.ID)
	if err != nil {
		return fmt.Errorf("get settings: %w", err)
	}
	if len(args) == 0 {
		state := "выключен"
		if s.WeeklyEnabled {
			state = "включён"
		}
		return h.d.API.Send(ctx, tgUserID,
			fmt.Sprintf("Еженедельный дайджест: %s. Использование: /weekly_settings on|off", state))
	}
	upd := postgres.SettingsUpdate{
		Topics:           s.Topics,
		Threshold:        s.Threshold,
		ScheduleJSON:     s.ScheduleJSON,
		AlertsEnabled:    s.AlertsEnabled,
		AlertThreshold:   s.AlertThreshold,
		AlertThrottleMin: s.AlertThrottleMin,
		WeeklyEnabled:    s.WeeklyEnabled,
	}
	switch strings.ToLower(args[0]) {
	case "on":
		upd.WeeklyEnabled = true
	case "off":
		upd.WeeklyEnabled = false
	default:
		return h.d.API.Send(ctx, tgUserID, "Использование: /weekly_settings on|off")
	}
	if err := h.d.Settings.Upsert(ctx, u.ID, upd); err != nil {
		return fmt.Errorf("upsert settings: %w", err)
	}
	state := "выключен"
	if upd.WeeklyEnabled {
		state = "включён"
	}
	return h.d.API.Send(ctx, tgUserID,
		fmt.Sprintf("Еженедельный дайджест %s.", state))
}

// HandleBuy implements /buy. When the Invoicer and ButtonAPI collaborators
// are wired (production), it asks billing for a Stars invoice URL and
// pushes a message with a single inline-keyboard button labelled
// "Купить за 99 ⭐ / мес" that opens the link. When either is nil
// (unit tests or partial wiring) it falls back to the M2 stub reply so
// the command stays well-defined.
//
// The Registrar (a users.Service) is consulted first so that paying users
// have a row before the Stars callback ever fires.
func (h *Handlers) HandleBuy(ctx context.Context, tgUserID int64, username string) error {
	if h.d.Invoicer == nil || h.d.ButtonAPI == nil {
		return h.d.API.Send(ctx, tgUserID,
			"Оплата подписки скоро будет доступна через Telegram Stars (99 XTR/мес). "+
				"Сейчас функция в разработке.")
	}
	if h.d.Registrar != nil {
		if _, _, err := h.d.Registrar.RegisterOnStart(ctx, tgUserID, username); err != nil {
			return fmt.Errorf("register user: %w", err)
		}
	}
	url, err := h.d.Invoicer.CreateInvoice(ctx, tgUserID, "pro_30d")
	if err != nil {
		return fmt.Errorf("create invoice: %w", err)
	}
	return h.d.ButtonAPI.SendURLButton(ctx, tgUserID,
		"Pro подписка — 99 ⭐ в месяц.\n\n"+
			"Включает кастомные темы, real-time alerts и безлимит /digest. "+
			"Нажмите кнопку ниже, чтобы оплатить.",
		"Купить за 99 ⭐ / мес",
		url,
	)
}

// HandleAlerts implements /alerts. The command is Pro-gated by the
// wiring layer (see RequirePro in middleware.go), so this body assumes
// the caller is already entitled. Subcommands:
//
//	/alerts             → show current state
//	/alerts on|off      → toggle delivery
//	/alerts threshold N → set the cluster-score gate (0..100)
//	/alerts throttle N  → set the per-topic cool-down (minutes, ≥1)
//
// Every write reads the existing settings first and copies through every
// field the user did not touch, so toggling alerts_enabled cannot wipe
// the digest schedule or the topic preset.
func (h *Handlers) HandleAlerts(ctx context.Context, tgUserID int64, username string, args []string) error {
	u, _, err := h.d.Users.GetOrCreate(ctx, tgUserID, username)
	if err != nil {
		return fmt.Errorf("get_or_create user: %w", err)
	}
	s, err := h.d.Settings.Get(ctx, u.ID)
	if err != nil {
		return fmt.Errorf("get settings: %w", err)
	}
	if len(args) == 0 {
		return h.d.API.Send(ctx, tgUserID, renderAlertsStatus(s))
	}

	upd := postgres.SettingsUpdate{
		Topics:           s.Topics,
		Threshold:        s.Threshold,
		ScheduleJSON:     s.ScheduleJSON,
		AlertsEnabled:    s.AlertsEnabled,
		AlertThreshold:   s.AlertThreshold,
		AlertThrottleMin: s.AlertThrottleMin,
		WeeklyEnabled:    s.WeeklyEnabled,
	}

	switch strings.ToLower(args[0]) {
	case "on":
		upd.AlertsEnabled = true
	case "off":
		upd.AlertsEnabled = false
	case "threshold":
		if len(args) < 2 {
			return h.d.API.Send(ctx, tgUserID, "Использование: /alerts threshold N (0..100)")
		}
		n, err := strconv.Atoi(args[1])
		if err != nil || n < 0 || n > 100 {
			return h.d.API.Send(ctx, tgUserID, "Порог alert'а должен быть от 0 до 100.")
		}
		upd.AlertThreshold = n
	case "throttle":
		if len(args) < 2 {
			return h.d.API.Send(ctx, tgUserID, "Использование: /alerts throttle N (минут)")
		}
		n, err := strconv.Atoi(args[1])
		if err != nil || n < 1 {
			return h.d.API.Send(ctx, tgUserID, "Throttle должен быть ≥ 1 минута.")
		}
		upd.AlertThrottleMin = n
	default:
		return h.d.API.Send(ctx, tgUserID,
			"Доступно: /alerts | /alerts on|off | /alerts threshold N | /alerts throttle N")
	}

	if err := h.d.Settings.Upsert(ctx, u.ID, upd); err != nil {
		return fmt.Errorf("upsert settings: %w", err)
	}
	// Re-read so the confirmation reflects the COALESCE defaults the repo
	// applies when N=0 — the user sees the same number the worker will use.
	after, err := h.d.Settings.Get(ctx, u.ID)
	if err != nil {
		return fmt.Errorf("get settings after upsert: %w", err)
	}
	return h.d.API.Send(ctx, tgUserID, renderAlertsStatus(after))
}

// renderAlertsStatus renders the human-readable summary of the alerts
// configuration. Kept as a package helper so HandleAlerts emits the same
// shape for the show-only path and the post-write confirmation.
func renderAlertsStatus(s postgres.Settings) string {
	state := "выключены"
	if s.AlertsEnabled {
		state = "включены"
	}
	return fmt.Sprintf(
		"Alerts: %s\nПорог: %d/100\nThrottle: %d мин",
		state, s.AlertThreshold, s.AlertThrottleMin,
	)
}

// HandleTopicsAdd implements /topics add NAME. Pro-only — the gate is
// applied by the wiring layer (RequirePro middleware). This body assumes
// the caller is already entitled and the Registrar has resolved/created
// the user row. If the Topics service is nil (partial wiring) the
// command falls back to a friendly stub so the bot never panics in dev.
//
// Errors mapped to user-facing messages:
//
//	topics.ErrTooManyTopics → "Лимит N кастомных тем"
//	topics.ErrEmptyName     → "Имя темы пустое"
func (h *Handlers) HandleTopicsAdd(ctx context.Context, tgUserID int64, username, name string) error {
	if h.d.Topics == nil {
		return h.d.API.Send(ctx, tgUserID, "Кастомные темы скоро будут доступны.")
	}
	u, _, err := h.d.Users.GetOrCreate(ctx, tgUserID, username)
	if err != nil {
		return fmt.Errorf("get_or_create user: %w", err)
	}
	if err := h.d.Topics.AddTopic(ctx, u.ID, name); err != nil {
		switch {
		case errors.Is(err, topics.ErrTooManyTopics):
			return h.d.API.Send(ctx, tgUserID,
				fmt.Sprintf("Лимит %d кастомных тем достигнут. Удалите одну через /topics remove.",
					topics.MaxTopicsPerUser))
		case errors.Is(err, topics.ErrEmptyName):
			return h.d.API.Send(ctx, tgUserID, "Имя темы не должно быть пустым.")
		case errors.Is(err, topics.ErrEmptyEmbedding):
			return h.d.API.Send(ctx, tgUserID,
				"Не удалось построить вектор для темы. Попробуйте позже.")
		}
		return fmt.Errorf("add topic: %w", err)
	}
	return h.d.API.Send(ctx, tgUserID, fmt.Sprintf("Тема %q добавлена.", strings.TrimSpace(name)))
}

// HandleTopicsRemove implements /topics remove NAME. Pro-only via the
// wiring layer. Idempotent at the service level — removing a missing
// row is silently treated as success so the user sees a consistent
// confirmation even on retries.
func (h *Handlers) HandleTopicsRemove(ctx context.Context, tgUserID int64, username, name string) error {
	if h.d.Topics == nil {
		return h.d.API.Send(ctx, tgUserID, "Кастомные темы скоро будут доступны.")
	}
	u, _, err := h.d.Users.GetOrCreate(ctx, tgUserID, username)
	if err != nil {
		return fmt.Errorf("get_or_create user: %w", err)
	}
	if err := h.d.Topics.RemoveTopic(ctx, u.ID, name); err != nil {
		if errors.Is(err, topics.ErrEmptyName) {
			return h.d.API.Send(ctx, tgUserID, "Использование: /topics remove <название>")
		}
		return fmt.Errorf("remove topic: %w", err)
	}
	return h.d.API.Send(ctx, tgUserID, fmt.Sprintf("Тема %q удалена.", strings.TrimSpace(name)))
}

// HandleTopicsList implements /topics list. Returns the names oldest-
// first; an empty list gets a hint pointing the user at /topics add.
func (h *Handlers) HandleTopicsList(ctx context.Context, tgUserID int64, username string) error {
	if h.d.Topics == nil {
		return h.d.API.Send(ctx, tgUserID, "Кастомные темы скоро будут доступны.")
	}
	u, _, err := h.d.Users.GetOrCreate(ctx, tgUserID, username)
	if err != nil {
		return fmt.Errorf("get_or_create user: %w", err)
	}
	names, err := h.d.Topics.ListTopics(ctx, u.ID)
	if err != nil {
		return fmt.Errorf("list topics: %w", err)
	}
	if len(names) == 0 {
		return h.d.API.Send(ctx, tgUserID,
			"У вас пока нет кастомных тем. Добавьте через /topics add <название>.")
	}
	var b strings.Builder
	b.WriteString("Ваши темы:\n")
	for _, n := range names {
		b.WriteString("• ")
		b.WriteString(n)
		b.WriteByte('\n')
	}
	return h.d.API.Send(ctx, tgUserID, strings.TrimRight(b.String(), "\n"))
}
