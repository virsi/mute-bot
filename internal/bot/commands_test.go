package bot

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/virsi/mute-bot/internal/storage/postgres"
)

// fakeUsers stubs UsersRepo. GetOrCreate always returns the same user row;
// the created flag is parameterised so individual tests can simulate either
// the first-touch (true) or returning-user (false) branch.
type fakeUsers struct {
	created bool
}

func (f *fakeUsers) GetOrCreate(_ context.Context, tg int64, username string) (postgres.User, bool, error) {
	return postgres.User{ID: 1, TGUserID: tg, Username: username, Tier: "free"}, f.created, nil
}

// fakeSettings stubs SettingsRepo. Upsert captures the last write so tests
// can assert the field set the handler chose; Get returns a fixed row so
// HandleThreshold/HandleToggleTopic can read the existing topics back.
type fakeSettings struct {
	stored postgres.SettingsUpdate
	get    postgres.Settings
}

func (f *fakeSettings) Upsert(_ context.Context, _ int64, in postgres.SettingsUpdate) error {
	f.stored = in
	return nil
}

func (f *fakeSettings) Get(_ context.Context, _ int64) (postgres.Settings, error) {
	return f.get, nil
}

// fakeAssembler stubs AssemblerIface. Records the call so HandleDigest tests
// can assert the assembler was invoked with the user's internal id.
type fakeAssembler struct {
	called bool
	req    AssembleReq
}

func (f *fakeAssembler) Assemble(_ context.Context, r AssembleReq) error {
	f.called = true
	f.req = r
	return nil
}

// capturedSender stubs the SendAPI: every Send is appended to msgs.
type capturedSender struct {
	msgs []string
}

func (c *capturedSender) Send(_ context.Context, _ int64, t string) error {
	c.msgs = append(c.msgs, t)
	return nil
}

func TestStart_CreatesUserAndSendsWelcome(t *testing.T) {
	send := &capturedSender{}
	st := &fakeSettings{}
	h := NewHandlers(HandlersDeps{Users: &fakeUsers{created: true}, Settings: st, API: send})

	require.NoError(t, h.HandleStart(context.Background(), 555, "alice"))
	require.Len(t, send.msgs, 1, "welcome message must be sent")
	// Newly-created users get default settings seeded.
	require.ElementsMatch(t, []string{"politics", "it"}, st.stored.Topics)
	require.Equal(t, 50, st.stored.Threshold)
	require.NotEmpty(t, st.stored.ScheduleJSON)
}

func TestStart_ExistingUserDoesNotResetSettings(t *testing.T) {
	send := &capturedSender{}
	st := &fakeSettings{}
	h := NewHandlers(HandlersDeps{Users: &fakeUsers{created: false}, Settings: st, API: send})

	require.NoError(t, h.HandleStart(context.Background(), 555, "alice"))
	require.Len(t, send.msgs, 1)
	require.Empty(t, st.stored.Topics, "settings must not be overwritten for returning users")
}

func TestDigest_DispatchesToAssembler(t *testing.T) {
	send := &capturedSender{}
	asm := &fakeAssembler{}
	h := NewHandlers(HandlersDeps{Users: &fakeUsers{}, Settings: &fakeSettings{}, Assembler: asm, API: send})

	require.NoError(t, h.HandleDigest(context.Background(), 555, "alice"))
	require.True(t, asm.called)
	require.Equal(t, int64(1), asm.req.UserID)
	require.Equal(t, int64(555), asm.req.TGUserID)
	require.Equal(t, "on_demand", asm.req.Channel)
}

func TestThreshold_Updates(t *testing.T) {
	send := &capturedSender{}
	st := &fakeSettings{get: postgres.Settings{Topics: []string{"politics"}, Threshold: 50}}
	h := NewHandlers(HandlersDeps{Users: &fakeUsers{}, Settings: st, API: send})

	require.NoError(t, h.HandleThreshold(context.Background(), 555, "alice", 70))
	require.Equal(t, 70, st.stored.Threshold)
	// Existing topics must be preserved on a threshold-only edit.
	require.Equal(t, []string{"politics"}, st.stored.Topics)
	require.Len(t, send.msgs, 1)
}

func TestThreshold_RejectsOutOfRange(t *testing.T) {
	send := &capturedSender{}
	st := &fakeSettings{get: postgres.Settings{Threshold: 50}}
	h := NewHandlers(HandlersDeps{Users: &fakeUsers{}, Settings: st, API: send})

	require.NoError(t, h.HandleThreshold(context.Background(), 555, "alice", 150))
	require.Equal(t, 0, st.stored.Threshold, "out-of-range value must not be stored")
	require.Len(t, send.msgs, 1)
}

func TestTopics_TogglesOn(t *testing.T) {
	send := &capturedSender{}
	st := &fakeSettings{get: postgres.Settings{
		Topics:       []string{"politics"},
		Threshold:    50,
		ScheduleJSON: json.RawMessage(`{}`),
	}}
	h := NewHandlers(HandlersDeps{Users: &fakeUsers{}, Settings: st, API: send})

	require.NoError(t, h.HandleToggleTopic(context.Background(), 555, "alice", "it"))
	require.ElementsMatch(t, []string{"politics", "it"}, st.stored.Topics)
}

func TestTopics_TogglesOff(t *testing.T) {
	send := &capturedSender{}
	st := &fakeSettings{get: postgres.Settings{Topics: []string{"politics", "it"}}}
	h := NewHandlers(HandlersDeps{Users: &fakeUsers{}, Settings: st, API: send})

	require.NoError(t, h.HandleToggleTopic(context.Background(), 555, "alice", "it"))
	require.Equal(t, []string{"politics"}, st.stored.Topics)
}

func TestSettings_RendersCurrentState(t *testing.T) {
	send := &capturedSender{}
	st := &fakeSettings{get: postgres.Settings{
		Topics:       []string{"politics", "it"},
		Threshold:    55,
		ScheduleJSON: json.RawMessage(`{"times":["08:00"],"tz":"Europe/Moscow"}`),
	}}
	h := NewHandlers(HandlersDeps{Users: &fakeUsers{}, Settings: st, API: send})

	require.NoError(t, h.HandleSettings(context.Background(), 555, "alice"))
	require.Len(t, send.msgs, 1)
	msg := send.msgs[0]
	require.Contains(t, msg, "55")
	require.Contains(t, msg, "Europe/Moscow")
}

func TestSchedule_UpdatesTimesAndTimezone(t *testing.T) {
	send := &capturedSender{}
	st := &fakeSettings{get: postgres.Settings{
		Topics:       []string{"politics"},
		Threshold:    50,
		ScheduleJSON: json.RawMessage(`{"times":["08:00","19:00"],"tz":"Europe/Moscow"}`),
	}}
	h := NewHandlers(HandlersDeps{Users: &fakeUsers{}, Settings: st, API: send})

	require.NoError(t, h.HandleSchedule(context.Background(), 555, "alice", "09:30,21:00", "Europe/Berlin"))
	require.NotEmpty(t, st.stored.ScheduleJSON, "schedule must be persisted")

	var got struct {
		Times []string `json:"times"`
		TZ    string   `json:"tz"`
	}
	require.NoError(t, json.Unmarshal(st.stored.ScheduleJSON, &got))
	require.Equal(t, []string{"09:30", "21:00"}, got.Times)
	require.Equal(t, "Europe/Berlin", got.TZ)
	// Other fields must be preserved (scheduler reload must see same topics/threshold).
	require.Equal(t, []string{"politics"}, st.stored.Topics)
	require.Equal(t, 50, st.stored.Threshold)
	require.Len(t, send.msgs, 1)
	require.Contains(t, send.msgs[0], "09:30")
	require.Contains(t, send.msgs[0], "21:00")
	require.Contains(t, send.msgs[0], "Europe/Berlin")
}

func TestSchedule_DefaultsToMoscowWhenTZEmpty(t *testing.T) {
	send := &capturedSender{}
	st := &fakeSettings{get: postgres.Settings{Threshold: 50}}
	h := NewHandlers(HandlersDeps{Users: &fakeUsers{}, Settings: st, API: send})

	require.NoError(t, h.HandleSchedule(context.Background(), 555, "alice", "08:00", ""))

	var got struct {
		Times []string `json:"times"`
		TZ    string   `json:"tz"`
	}
	require.NoError(t, json.Unmarshal(st.stored.ScheduleJSON, &got))
	require.Equal(t, []string{"08:00"}, got.Times)
	require.Equal(t, "Europe/Moscow", got.TZ)
}

func TestSchedule_RejectsMalformedTime(t *testing.T) {
	send := &capturedSender{}
	st := &fakeSettings{get: postgres.Settings{Threshold: 50}}
	h := NewHandlers(HandlersDeps{Users: &fakeUsers{}, Settings: st, API: send})

	require.NoError(t, h.HandleSchedule(context.Background(), 555, "alice", "08:00,25:99", "Europe/Moscow"))
	require.Empty(t, st.stored.ScheduleJSON, "invalid time must not be persisted")
	require.Len(t, send.msgs, 1)
}

func TestSchedule_RejectsEmptyTimes(t *testing.T) {
	send := &capturedSender{}
	st := &fakeSettings{get: postgres.Settings{Threshold: 50}}
	h := NewHandlers(HandlersDeps{Users: &fakeUsers{}, Settings: st, API: send})

	require.NoError(t, h.HandleSchedule(context.Background(), 555, "alice", "", "Europe/Moscow"))
	require.Empty(t, st.stored.ScheduleJSON)
	require.Len(t, send.msgs, 1)
}

func TestSchedule_PersistsForSchedulerReload(t *testing.T) {
	// Scheduler reads settings via SettingsRepo.Get at each tick. Simulate the
	// reload by reading the stored value back through Get after HandleSchedule.
	send := &capturedSender{}
	st := &fakeSettings{get: postgres.Settings{Topics: []string{"it"}, Threshold: 60}}
	h := NewHandlers(HandlersDeps{Users: &fakeUsers{}, Settings: st, API: send})

	require.NoError(t, h.HandleSchedule(context.Background(), 555, "alice", "07:00,18:30", "Europe/Moscow"))

	// Now flip Get to return what was just stored — what the scheduler would see.
	st.get = postgres.Settings{
		Topics:       st.stored.Topics,
		Threshold:    st.stored.Threshold,
		ScheduleJSON: st.stored.ScheduleJSON,
	}
	reloaded, err := st.Get(context.Background(), 1)
	require.NoError(t, err)

	var got struct {
		Times []string `json:"times"`
		TZ    string   `json:"tz"`
	}
	require.NoError(t, json.Unmarshal(reloaded.ScheduleJSON, &got))
	require.Equal(t, []string{"07:00", "18:30"}, got.Times)
	require.Equal(t, "Europe/Moscow", got.TZ)
}

func TestAssemblerFunc_AdaptsFunc(t *testing.T) {
	called := false
	var f AssemblerFunc = func(_ context.Context, _ AssembleReq) error {
		called = true
		return nil
	}
	require.NoError(t, f.Assemble(context.Background(), AssembleReq{}))
	require.True(t, called)
}
