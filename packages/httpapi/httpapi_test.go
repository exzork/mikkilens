package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/exzork/mikkilens/packages/audio/capture"
	"github.com/exzork/mikkilens/packages/audio/devices"
	"github.com/exzork/mikkilens/packages/audio/feedback"
	"github.com/exzork/mikkilens/packages/audio/hotkey"
	"github.com/exzork/mikkilens/packages/audio/stt"
	"github.com/exzork/mikkilens/packages/audio/tts"
	"github.com/exzork/mikkilens/packages/audio/wake"
	"github.com/exzork/mikkilens/packages/chat"
	"github.com/exzork/mikkilens/packages/controllers/llm"
	"github.com/exzork/mikkilens/packages/controllers/obs"
	"github.com/exzork/mikkilens/packages/controllers/youtube"
	"github.com/exzork/mikkilens/packages/core/config"
	"github.com/exzork/mikkilens/packages/core/i18n"
	"github.com/exzork/mikkilens/packages/core/intent"
	"github.com/exzork/mikkilens/packages/core/paths"
	"github.com/exzork/mikkilens/packages/core/state"
	"github.com/exzork/mikkilens/packages/httpapi"
)

// The regression that matters most here is that request bodies actually bind,
// and that a rejected write never reaches disk. A settings page that reports
// success while changing nothing is worse than one that fails loudly.

// stubEngine is enough of the engine for the routes, with no audio and no
// network.
type stubEngine struct {
	settings config.Config
	locale   *i18n.Locale
	store    *state.Store
	bus      *feedback.Bus
	commands *intent.Set
	router   *intent.Router

	applied []config.Config
	adopted []*intent.Set
	reloads int
	listens int
	ran     []ranCommand
}

// ranCommand records a command a button or a key asked for.
type ranCommand struct {
	id      string
	confirm bool
}

type silentPlayer struct{}

func (silentPlayer) Play(tts.Audio) (bool, error) { return true, nil }
func (silentPlayer) Stop()                        {}
func (silentPlayer) SetDevice(*devices.Device)    {}

func newStubEngine(t *testing.T) *stubEngine {
	t.Helper()
	locale := i18n.Load("id")
	settings := config.Default()

	commands, err := intent.SetFromFile(shippedCommands(t))
	if err != nil {
		t.Fatalf("could not load the shipped commands: %v", err)
	}

	bus := feedback.NewWith(settings, locale, silentPlayer{},
		func(context.Context, string, tts.Options) (tts.Audio, error) {
			return tts.Audio{SampleRate: 48000, Channels: 1}, nil
		})

	return &stubEngine{
		settings: settings,
		locale:   locale,
		store:    state.New(),
		bus:      bus,
		commands: commands,
		router:   intent.NewRouter(commands, bus, locale, time.Second),
	}
}

// shippedCommands finds commands.id.toml in the repository, before the test
// redirects the project root at a temporary directory.
func shippedCommands(t *testing.T) string {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		candidate := filepath.Join(directory, "commands.id.toml")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			t.Fatal("could not find commands.id.toml")
		}
		directory = parent
	}
}

func (e *stubEngine) Config() config.Config { return e.settings }
func (e *stubEngine) ApplyConfig(updated config.Config) error {
	e.settings = updated
	e.applied = append(e.applied, updated)
	return nil
}
func (e *stubEngine) Locale() *i18n.Locale  { return e.locale }
func (e *stubEngine) State() *state.Store   { return e.store }
func (e *stubEngine) Bus() *feedback.Bus    { return e.bus }
func (e *stubEngine) Commands() *intent.Set { return e.commands }
func (e *stubEngine) AdoptCommands(set *intent.Set) {
	e.commands = set
	e.adopted = append(e.adopted, set)
}
func (e *stubEngine) ReloadCommands()               { e.reloads++ }
func (e *stubEngine) Router() *intent.Router        { return e.router }
func (e *stubEngine) Transcriber() *stt.Transcriber { return stt.New(e.settings.STT, "id") }
func (e *stubEngine) Wake() *wake.Detector          { return nil }
func (e *stubEngine) Hotkey() hotkey.Watcher        { return nil }
func (e *stubEngine) Microphone() *capture.Stream   { return nil }
func (e *stubEngine) OBS() *obs.Controller          { return nil }
func (e *stubEngine) YouTube() *youtube.Controller  { return nil }
func (e *stubEngine) ChatIngest() *chat.Ingest      { return nil }
func (e *stubEngine) ChatReader() *chat.Reader      { return nil }
func (e *stubEngine) BeginListening()               { e.listens++ }
func (e *stubEngine) RunCommand(id string, confirm bool) {
	e.ran = append(e.ran, ranCommand{id: id, confirm: confirm})
}
func (e *stubEngine) OnYouTubeConnected(context.Context) {}
func (e *stubEngine) OnYouTubeDisconnected()             {}
func (e *stubEngine) RefreshYouTube(context.Context)     {}
func (e *stubEngine) OnMatcherProgress(llm.Progress)     {}

// client wires a stub engine to a running test server, with every write
// contained in a temporary directory.
func client(t *testing.T) (*httptest.Server, *stubEngine, string) {
	t.Helper()
	engine := newStubEngine(t)

	directory := t.TempDir()
	// Seed a command file so the routes have something to rewrite.
	shipped, err := os.ReadFile(shippedCommands(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(directory, "commands.id.toml"), shipped, 0o644); err != nil {
		t.Fatal(err)
	}
	paths.SetRoot(directory)

	server := httptest.NewServer(httpapi.NewServer(engine).Handler())
	t.Cleanup(server.Close)
	return server, engine, directory
}

func get(t *testing.T, server *httptest.Server, path string) map[string]any {
	t.Helper()
	response, err := http.Get(server.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET %s returned %s", path, response.Status)
	}

	var payload map[string]any
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return payload
}

func send(t *testing.T, server *httptest.Server, method, path string, body any) (int, map[string]any) {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(method, server.URL+path, bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()

	var payload map[string]any
	_ = json.NewDecoder(response.Body).Decode(&payload)
	return response.StatusCode, payload
}

// -- reads --------------------------------------------------------------------

func TestStateEndpointReportsTheSnapshot(t *testing.T) {
	server, _, _ := client(t)
	body := get(t, server, "/api/state")

	if body["obs"] != "unknown" {
		t.Errorf("obs = %v", body["obs"])
	}
	if count, _ := body["command_count"].(float64); count <= 0 {
		t.Errorf("command_count = %v", body["command_count"])
	}
}

func TestConfigEndpointListsLanguages(t *testing.T) {
	server, _, _ := client(t)
	body := get(t, server, "/api/config")

	languages, _ := body["_languages"].([]any)
	found := map[string]bool{}
	for _, language := range languages {
		found[language.(string)] = true
	}
	if !found["id"] || !found["en"] {
		t.Errorf("_languages = %v", languages)
	}

	language, _ := body["language"].(map[string]any)
	if language["output"] != "id" {
		t.Errorf("language.output = %v", language["output"])
	}
}

func TestCommandsEndpointListsPhrases(t *testing.T) {
	server, _, _ := client(t)
	body := get(t, server, "/api/commands")

	commands, _ := body["commands"].(map[string]any)
	muteMic, ok := commands["mute_mic"].(map[string]any)
	if !ok {
		t.Fatalf("mute_mic is missing from %v", commands)
	}
	if phrases, _ := muteMic["phrases"].([]any); len(phrases) == 0 {
		t.Error("mute_mic has no phrases")
	}
}

// TestCommandsEndpointKeepsFileOrder matters because that order is what "what
// can I say" reads out.
func TestCommandsEndpointKeepsFileOrder(t *testing.T) {
	server, _, _ := client(t)
	order, _ := get(t, server, "/api/commands")["order"].([]any)
	if len(order) == 0 {
		t.Fatal("no order was returned")
	}
	if order[0] != "go_live" {
		t.Errorf("the first command is %v, want go_live as the file has it", order[0])
	}
}

func TestHealthEndpointAnswers(t *testing.T) {
	server, _, _ := client(t)
	if body := get(t, server, "/api/health"); body["ok"] != true {
		t.Errorf("health = %v", body)
	}
}

// -- writes -------------------------------------------------------------------

func TestConfigWriteBindsItsBodyAndAppliesLive(t *testing.T) {
	server, engine, _ := client(t)

	status, _ := send(t, server, http.MethodPut, "/api/config", map[string]any{
		"speech": map[string]any{"chat_rate": "+40%"},
	})
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	if engine.settings.Speech.ChatRate != "+40%" {
		t.Errorf("chat_rate = %q", engine.settings.Speech.ChatRate)
	}
	if len(engine.applied) == 0 {
		t.Error("the change must be applied to the running app, not just saved")
	}
}

func TestConfigWriteKeepsUnmentionedValues(t *testing.T) {
	server, engine, _ := client(t)
	send(t, server, http.MethodPut, "/api/config", map[string]any{
		"speech": map[string]any{"rate": "+5%"},
	})
	if engine.settings.Language.Output != "id" {
		t.Errorf("language.output = %q", engine.settings.Language.Output)
	}
	if engine.settings.OBS.Port != 4455 {
		t.Errorf("obs.port = %d", engine.settings.OBS.Port)
	}
}

func TestUnknownConfigSectionIsIgnoredNotFatal(t *testing.T) {
	server, _, _ := client(t)
	status, _ := send(t, server, http.MethodPut, "/api/config", map[string]any{
		"nonsense": map[string]any{"a": 1},
	})
	if status != http.StatusOK {
		t.Errorf("status = %d, want the unknown section to be ignored", status)
	}
}

func TestCommandsWriteBindsItsBody(t *testing.T) {
	server, engine, _ := client(t)

	status, body := send(t, server, http.MethodPut, "/api/commands", map[string]any{
		"commands": map[string]any{
			"mute_mic": map[string]any{
				"phrases": []string{"matikan mikrofon"}, "confirm": false,
			},
		},
	})
	if status != http.StatusOK {
		t.Fatalf("status = %d: %v", status, body)
	}
	if count, _ := body["count"].(float64); count != 1 {
		t.Errorf("count = %v", body["count"])
	}
	if len(engine.adopted) == 0 {
		t.Error("the new command set must be adopted by the running app")
	}
}

func TestSecretWriteBindsItsBody(t *testing.T) {
	server, _, directory := client(t)

	status, _ := send(t, server, http.MethodPut, "/api/secret", map[string]any{
		"name": "MY_KEY", "value": "abc",
	})
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	stored, err := os.ReadFile(filepath.Join(directory, "data", "secrets.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(stored), "abc") {
		t.Errorf("the secret did not reach the file: %s", stored)
	}
}

func TestSpeakBindsItsBody(t *testing.T) {
	server, _, _ := client(t)
	if status, _ := send(t, server, http.MethodPost, "/api/speak",
		map[string]any{"text": "halo"}); status != http.StatusOK {
		t.Errorf("status = %d", status)
	}
}

func TestSpeakRefusesEmptyText(t *testing.T) {
	server, _, _ := client(t)
	if status, _ := send(t, server, http.MethodPost, "/api/speak",
		map[string]any{"text": "   "}); status != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", status)
	}
}

func TestListenTriggersTheEngine(t *testing.T) {
	server, engine, _ := client(t)
	send(t, server, http.MethodPost, "/api/listen", nil)
	if engine.listens != 1 {
		t.Errorf("listens = %d", engine.listens)
	}
}

// -- running a command from a button ------------------------------------------

// A Stream Deck key, a mouse macro and a phone on the desk all arrive at this
// endpoint. It has to reach the same command the voice would, keep the
// confirmation gate unless the caller waives it, and refuse a name that does
// not exist rather than quietly doing nothing.

func TestACommandCanBeRunWithoutSpeaking(t *testing.T) {
	server, engine, _ := client(t)

	status, _ := send(t, server, http.MethodPost, "/api/command",
		map[string]any{"command": "go_live"})
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if len(engine.ran) != 1 {
		t.Fatalf("ran = %v, want one command", engine.ran)
	}
	if engine.ran[0].id != "go_live" {
		t.Errorf("ran %q, want go_live", engine.ran[0].id)
	}
	if !engine.ran[0].confirm {
		t.Error("a command must keep its own confirmation unless the caller waives it")
	}
}

func TestAButtonCanWaiveTheConfirmation(t *testing.T) {
	server, engine, _ := client(t)

	send(t, server, http.MethodPost, "/api/command",
		map[string]any{"command": "stop_stream", "confirm": false})
	if len(engine.ran) != 1 || engine.ran[0].confirm {
		t.Fatalf("ran = %v, want stop_stream without confirmation", engine.ran)
	}
}

func TestAnUnknownCommandIsRefused(t *testing.T) {
	server, engine, _ := client(t)

	status, _ := send(t, server, http.MethodPost, "/api/command",
		map[string]any{"command": "make_coffee"})
	if status != http.StatusNotFound {
		t.Errorf("status = %d, want 404", status)
	}
	if len(engine.ran) != 0 {
		t.Errorf("ran = %v, want nothing", engine.ran)
	}
}

func TestACommandNeedsAName(t *testing.T) {
	server, _, _ := client(t)

	status, _ := send(t, server, http.MethodPost, "/api/command",
		map[string]any{"command": "   "})
	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", status)
	}
}

// -- validation ---------------------------------------------------------------

func TestACommandsFileWithNothingUsableIsRejected(t *testing.T) {
	server, _, directory := client(t)
	before, err := os.ReadFile(filepath.Join(directory, "commands.id.toml"))
	if err != nil {
		t.Fatal(err)
	}

	status, _ := send(t, server, http.MethodPut, "/api/commands", map[string]any{
		"commands": map[string]any{"x": map[string]any{"phrases": []string{}}},
	})
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", status)
	}

	after, err := os.ReadFile(filepath.Join(directory, "commands.id.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Error("a rejected save must not touch the file")
	}
}

func TestARejectedSaveLeavesThePreviousFileIntact(t *testing.T) {
	server, _, directory := client(t)
	path := filepath.Join(directory, "commands.id.toml")

	send(t, server, http.MethodPut, "/api/commands", map[string]any{
		"commands": map[string]any{"a": map[string]any{"phrases": []string{"halo dunia"}}},
	})
	good, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	send(t, server, http.MethodPut, "/api/commands", map[string]any{
		"commands": map[string]any{"x": map[string]any{"phrases": []string{}}},
	})
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(good, after) {
		t.Error("the previous file must survive a rejected save")
	}
}

func TestSavedCommandsKeepAReadableHeader(t *testing.T) {
	server, _, directory := client(t)
	send(t, server, http.MethodPut, "/api/commands", map[string]any{
		"commands": map[string]any{"a": map[string]any{"phrases": []string{"halo dunia"}}},
	})

	written, err := os.ReadFile(filepath.Join(directory, "commands.id.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(written), "#") {
		t.Error("the file must stay self-explanatory for hand editing")
	}
}

// TestSavedCommandsCanBeReloaded is the round trip that matters: what the
// settings app writes has to be something MikkiLens can read back.
func TestSavedCommandsCanBeReloaded(t *testing.T) {
	server, _, directory := client(t)
	send(t, server, http.MethodPut, "/api/commands", map[string]any{
		"commands": map[string]any{
			"mute_mic": map[string]any{
				"phrases": []string{"matikan mikrofon", "mute mic"}, "confirm": false,
			},
			"stop_stream": map[string]any{
				"phrases": []string{"hentikan siaran"},
				"confirm": true, "confirm_prompt": "confirm.stop_stream",
			},
		},
	})

	reloaded, err := intent.SetFromFile(filepath.Join(directory, "commands.id.toml"))
	if err != nil {
		t.Fatalf("the saved file could not be read back: %v", err)
	}
	if reloaded.Len() != 2 {
		t.Errorf("reloaded %d commands, want 2", reloaded.Len())
	}
	if !reloaded.Commands["stop_stream"].Confirm {
		t.Error("the confirm flag was lost in the round trip")
	}
	if reloaded.Commands["stop_stream"].ConfirmPrompt != "confirm.stop_stream" {
		t.Error("the confirm prompt was lost in the round trip")
	}
}

func TestWrongMethodIsReportedClearly(t *testing.T) {
	server, _, _ := client(t)
	status, body := send(t, server, http.MethodPost, "/api/state", nil)
	if status != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", status)
	}
	if body["detail"] == nil {
		t.Error("a refusal should explain itself")
	}
}

// -- websocket ----------------------------------------------------------------

func TestWebSocketSendsASnapshotThenDeltas(t *testing.T) {
	server, engine, _ := client(t)

	address := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	connection, _, err := websocket.DefaultDialer.Dial(address, nil)
	if err != nil {
		t.Fatalf("could not connect: %v", err)
	}
	defer connection.Close()

	_ = connection.SetReadDeadline(time.Now().Add(5 * time.Second))

	var first struct {
		Type string         `json:"type"`
		Data map[string]any `json:"data"`
	}
	if err := connection.ReadJSON(&first); err != nil {
		t.Fatalf("reading the snapshot: %v", err)
	}
	if first.Type != "snapshot" {
		t.Fatalf("first message is %q, want snapshot", first.Type)
	}

	engine.store.Update(state.Changes{"viewer_count": 7})

	var second struct {
		Type string         `json:"type"`
		Data map[string]any `json:"data"`
	}
	if err := connection.ReadJSON(&second); err != nil {
		t.Fatalf("reading the delta: %v", err)
	}
	if second.Type != "delta" {
		t.Fatalf("second message is %q, want delta", second.Type)
	}
	if count, _ := second.Data["viewer_count"].(float64); count != 7 {
		t.Errorf("delta = %v", second.Data)
	}
}

// TestWebSocketDoesNotBlockTheStateStore covers the case that would matter
// mid-stream: a window that has stopped reading must not stall the voice path.
func TestWebSocketDoesNotBlockTheStateStore(t *testing.T) {
	server, engine, _ := client(t)

	address := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	connection, _, err := websocket.DefaultDialer.Dial(address, nil)
	if err != nil {
		t.Fatalf("could not connect: %v", err)
	}
	defer connection.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for index := 0; index < 500; index++ {
			engine.store.Update(state.Changes{"viewer_count": index})
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the state store blocked on a socket nobody was reading")
	}
}

// -- startup ------------------------------------------------------------------

func TestStartupReportsItsState(t *testing.T) {
	server, _, _ := client(t)
	if _, ok := get(t, server, "/api/startup")["enabled"].(bool); !ok {
		t.Error("startup should report a boolean")
	}
}

// -- stream control -----------------------------------------------------------

// Ending a stream is not undoable, so the endpoint says which state it wants
// rather than toggling. A toggle acts on state the caller cannot see, which is
// how a live stream gets ended by accident.
func TestStoppingAStreamWithNoOBSSaysSoRatherThanSucceeding(t *testing.T) {
	server, _, _ := client(t)

	status, payload := send(t, server, http.MethodPost, "/api/obs/stream",
		map[string]any{"active": false})

	if status == http.StatusOK {
		t.Fatal("a stream cannot be stopped while OBS is disconnected; " +
			"reporting success would leave her believing it stopped")
	}
	if status != http.StatusServiceUnavailable {
		t.Errorf("status is %d, want %d", status, http.StatusServiceUnavailable)
	}
	if payload["detail"] == nil {
		t.Error("the refusal must say why")
	}
}

func TestStartingAStreamWithNoOBSSaysSoRatherThanSucceeding(t *testing.T) {
	server, _, _ := client(t)

	status, _ := send(t, server, http.MethodPost, "/api/obs/stream",
		map[string]any{"active": true})

	if status != http.StatusServiceUnavailable {
		t.Errorf("status is %d, want %d", status, http.StatusServiceUnavailable)
	}
}

// Starting or stopping a broadcast must not be reachable by anything that
// merely follows a link.
func TestStreamControlRejectsAGetRequest(t *testing.T) {
	server, _, _ := client(t)

	status, _ := send(t, server, http.MethodGet, "/api/obs/stream", nil)

	if status != http.StatusMethodNotAllowed {
		t.Errorf("status is %d, want %d", status, http.StatusMethodNotAllowed)
	}
}
