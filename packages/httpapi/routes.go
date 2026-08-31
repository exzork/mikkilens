package httpapi

import (
	"context"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/exzork/mikkilens/packages/audio/devices"
	"github.com/exzork/mikkilens/packages/audio/feedback"
	"github.com/exzork/mikkilens/packages/audio/tts"
	"github.com/exzork/mikkilens/packages/controllers/llm"
	"github.com/exzork/mikkilens/packages/controllers/vision"
	"github.com/exzork/mikkilens/packages/core/config"
	"github.com/exzork/mikkilens/packages/core/i18n"
	"github.com/exzork/mikkilens/packages/core/intent"
	"github.com/exzork/mikkilens/packages/core/paths"
)

// commandsHeader is written back above a saved command file. The file is meant
// to be read and edited by hand, and a TOML encoder cannot preserve comments,
// so a fresh header goes with every save.
const commandsHeader = `# Perintah suara MikkiLens.
#
# Berkas ini boleh diubah dengan tangan, atau lewat aplikasi Pengaturan.
# Kalau sebuah perintah sering salah didengar, tambahkan kalimat yang salah
# didengar itu ke daftar ` + "`phrases`" + ` -- itu cara paling cepat memperbaikinya.
# Setelah menyimpan, ucapkan "muat ulang perintah".
#
# {scene}, {source}, {text}, {question} adalah isian yang berubah-ubah.
#
# Catatan: menyimpan lewat aplikasi Pengaturan menulis ulang berkas ini, jadi
# komentar tambahan yang kamu tulis sendiri akan hilang.

`

func (s *Server) routes(mux *http.ServeMux) {
	mux.HandleFunc("/ws", s.handleWebSocket)
	mux.HandleFunc("/api/health", only(http.MethodGet, s.getHealth))

	mux.HandleFunc("/api/state", only(http.MethodGet, s.getState))
	mux.HandleFunc("/api/log", only(http.MethodGet, s.getLog))

	mux.HandleFunc("/api/devices", only(http.MethodGet, s.getDevices))
	mux.HandleFunc("/api/devices/test", only(http.MethodPost, s.testDevice))
	mux.HandleFunc("/api/voices", only(http.MethodGet, s.getVoices))
	mux.HandleFunc("/api/speak", only(http.MethodPost, s.speak))

	mux.HandleFunc("/api/config", s.handleConfig)
	mux.HandleFunc("/api/secret", only(http.MethodPut, s.putSecret))

	mux.HandleFunc("/api/commands", s.handleCommands)
	mux.HandleFunc("/api/commands/reload", only(http.MethodPost, s.reloadCommands))

	mux.HandleFunc("/api/test/obs", only(http.MethodPost, s.testOBS))
	mux.HandleFunc("/api/obs/stream", only(http.MethodPost, s.setStreaming))
	mux.HandleFunc("/api/test/vision", only(http.MethodPost, s.testVision))

	mux.HandleFunc("/api/youtube/status", only(http.MethodGet, s.youtubeStatus))
	mux.HandleFunc("/api/youtube/connect", only(http.MethodPost, s.youtubeConnect))
	mux.HandleFunc("/api/youtube/disconnect", only(http.MethodPost, s.youtubeDisconnect))

	mux.HandleFunc("/api/matcher/status", only(http.MethodGet, s.matcherStatus))
	mux.HandleFunc("/api/matcher/download", only(http.MethodPost, s.matcherDownload))
	mux.HandleFunc("/api/matcher/cancel", only(http.MethodPost, s.matcherCancel))

	mux.HandleFunc("/api/startup", s.handleStartup)
	mux.HandleFunc("/api/listen", only(http.MethodPost, s.triggerListen))
	mux.HandleFunc("/api/command", only(http.MethodPost, s.runCommand))
}

// setStreaming starts or stops the broadcast in OBS.
//
// The same thing is voice-operable ("mulai siaran", "hentikan siaran"), and
// voice is the point of this application -- but the settings page is where
// someone helping her works, and asking them to speak a command into her
// microphone to end a stream is worse than a button.
//
// Stopping is deliberately not a toggle. A single "streaming" switch that
// means start or stop depending on state it cannot see is exactly how a stream
// gets ended by accident, so the caller says which one it wants.
func (s *Server) setStreaming(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Active bool `json:"active"`
	}
	if !decode(writer, request, &body) {
		return
	}

	controller := s.engine.OBS()
	if controller == nil || !controller.Connected() {
		fail(writer, http.StatusServiceUnavailable, "OBS is not connected")
		return
	}

	live, err := controller.IsStreaming()
	if err != nil {
		fail(writer, http.StatusBadGateway, err.Error())
		return
	}
	if live == body.Active {
		// Already in the asked-for state. Reported as success with a note
		// rather than an error: nothing is wrong, there is just nothing to do.
		respond(writer, http.StatusOK, map[string]any{
			"ok": true, "streaming": live, "unchanged": true,
		})
		return
	}

	if body.Active {
		err = controller.StartStream()
	} else {
		err = controller.StopStream()
	}
	if err != nil {
		fail(writer, http.StatusBadGateway, err.Error())
		return
	}

	respond(writer, http.StatusOK, map[string]any{
		"ok": true, "streaming": body.Active, "unchanged": false,
	})
}

// -- the command matcher ------------------------------------------------------

// installer is shared, so a download survives the settings page being closed
// and reopened, and two pages cannot start the same one twice.
var installer = llm.NewInstaller()

func (s *Server) matcherStatus(writer http.ResponseWriter, _ *http.Request) {
	settings := s.engine.Config()
	progress := installer.Progress()

	base, model, _ := settings.MatcherEndpoint()
	respond(writer, http.StatusOK, map[string]any{
		"enabled":           settings.Matcher.Enabled,
		"models":            llm.MarshalModels(),
		"installed_model":   llm.InstalledModel(),
		"runtime_installed": llm.RuntimeInstalled(),
		"loading":           llm.Bundled().Loading(),
		"ready":             llm.Bundled().BaseURL() != "",
		// Whether the running model can also describe the screen, and whether
		// vision is currently falling back to it.
		"vision":          llm.Bundled().Vision(),
		"vision_is_local": !settings.Vision.Configured() && llm.Bundled().Vision(),
		"downloading":     installer.Running(),
		"progress":        progress,
		// A base URL she set herself takes precedence over the bundled model,
		// and the page should say so rather than showing both as active.
		"external_base_url": base,
		"external_model":    model,
	})
}

// matcherDownload starts fetching a model. It answers at once: the download is
// gigabytes and nothing should be waiting on it.
func (s *Server) matcherDownload(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Model string `json:"model"`
	}
	if !decode(writer, request, &body) {
		return
	}

	model, ok := llm.ModelByName(body.Model)
	if !ok {
		fail(writer, http.StatusBadRequest, "no such model: "+body.Model)
		return
	}

	if err := installer.Install(model, s.engine.OnMatcherProgress); err != nil {
		fail(writer, http.StatusConflict, err.Error())
		return
	}
	respond(writer, http.StatusOK, map[string]any{
		"ok": true, "model": model.Name, "bytes": model.Bytes,
	})
}

func (s *Server) matcherCancel(writer http.ResponseWriter, _ *http.Request) {
	installer.Cancel()
	respond(writer, http.StatusOK, map[string]any{"ok": true})
}

// -- state --------------------------------------------------------------------

func (s *Server) getHealth(writer http.ResponseWriter, _ *http.Request) {
	respond(writer, http.StatusOK, map[string]any{"ok": true, "app": "mikkilens"})
}

func (s *Server) getState(writer http.ResponseWriter, _ *http.Request) {
	respond(writer, http.StatusOK, s.fullSnapshot())
}

// fullSnapshot is the live state plus the things that are not state exactly --
// which backend recognition ended up on, which wake word loaded, how many
// commands are defined.
//
// The socket sends this too, not just the bare state. A client that connected
// and then only listened would otherwise show blanks for half the status page,
// which is the page she opens when something is wrong.
func (s *Server) fullSnapshot() map[string]any {
	snapshot := map[string]any{}
	for key, value := range s.engine.State().Snapshot() {
		snapshot[key] = value
	}

	snapshot["stt_backend"] = s.engine.Transcriber().Describe()
	snapshot["stt_loaded"] = s.engine.Transcriber().Loaded()
	snapshot["command_count"] = s.engine.Commands().Len()
	snapshot["unhandled"] = orEmpty(s.engine.Router().UnhandledCommands())

	if detector := s.engine.Wake(); detector != nil {
		snapshot["wake_model"] = detector.ModelName()
		snapshot["wake_score"] = detector.LastScore()
	} else {
		snapshot["wake_model"] = nil
	}
	if watcher := s.engine.Hotkey(); watcher != nil {
		snapshot["hotkey"] = watcher.Combination()
	} else {
		snapshot["hotkey"] = nil
	}
	if microphone := s.engine.Microphone(); microphone != nil {
		snapshot["mic_frames"] = microphone.FramesSeen()
		snapshot["mic_error"] = microphone.LastError()
	}
	return snapshot
}

// getLog is the diagnosis page: what MikkiLens heard, and what it said back.
// Almost every problem is visible from here.
func (s *Server) getLog(writer http.ResponseWriter, request *http.Request) {
	limit := 100
	if raw := request.URL.Query().Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			limit = parsed
		}
	}

	history := s.engine.Bus().History()
	if len(history) > limit {
		history = history[len(history)-limit:]
	}
	// Newest first: that is the line she is asking about.
	reversed := make([]feedback.Spoken, 0, len(history))
	for index := len(history) - 1; index >= 0; index-- {
		reversed = append(reversed, history[index])
	}

	respond(writer, http.StatusOK, map[string]any{
		"spoken":           reversed,
		"last_transcript":  s.engine.State().Get("last_transcript"),
		"last_command":     s.engine.State().Get("last_command"),
		"command_warnings": orEmpty(s.engine.Commands().Warnings),
	})
}

// -- audio --------------------------------------------------------------------

func (s *Server) getDevices(writer http.ResponseWriter, _ *http.Request) {
	outputs, outErr := devices.List(devices.Output)
	inputs, inErr := devices.List(devices.Input)

	payload := map[string]any{
		"output":   outputs,
		"input":    inputs,
		"host_api": devices.HostAPI(),
	}
	if outErr != nil || inErr != nil {
		payload["error"] = "some audio devices could not be listed"
	}
	respond(writer, http.StatusOK, payload)
}

func (s *Server) testDevice(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Index int `json:"index"`
	}
	if !decode(writer, request, &body) {
		return
	}

	found, err := devices.List(devices.Output)
	if err != nil {
		fail(writer, http.StatusInternalServerError, err.Error())
		return
	}
	for index := range found {
		if found[index].Index != body.Index {
			continue
		}
		volume := s.engine.Config().Speech.EarconVolume + 0.1
		if err := devices.PlayTestTone(&found[index], volume); err != nil {
			fail(writer, http.StatusInternalServerError, err.Error())
			return
		}
		respond(writer, http.StatusOK, map[string]any{"ok": true, "played": found[index].Name})
		return
	}
	fail(writer, http.StatusNotFound, "no such output device")
}

var (
	voiceOnce  sync.Once
	voiceCache []tts.Voice
)

func (s *Server) getVoices(writer http.ResponseWriter, request *http.Request) {
	voiceOnce.Do(func() {
		ctx, cancel := context.WithTimeout(request.Context(), 20*time.Second)
		defer cancel()

		voices, err := tts.ListVoices(ctx)
		if err != nil {
			// Being offline means she picks from what is already configured,
			// not that the page breaks.
			voiceCache = nil
			return
		}
		voiceCache = voices
	})

	prefix := strings.ToLower(request.URL.Query().Get("language"))
	if prefix == "" {
		prefix = strings.ToLower(s.engine.Config().Language.Output)
	}

	matching := []tts.Voice{}
	for _, voice := range voiceCache {
		if prefix == "" || strings.HasPrefix(strings.ToLower(voice.Locale), prefix) {
			matching = append(matching, voice)
		}
	}
	respond(writer, http.StatusOK, matching)
}

func (s *Server) speak(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Text  string `json:"text"`
		Voice string `json:"voice"`
	}
	if !decode(writer, request, &body) {
		return
	}
	if strings.TrimSpace(body.Text) == "" {
		fail(writer, http.StatusBadRequest, "there is nothing to say")
		return
	}

	settings := s.engine.Config()
	voice := body.Voice
	if voice == "" {
		voice = settings.Voice(s.engine.Locale().DefaultVoice())
	}
	s.engine.Bus().Enqueue(feedback.Utterance{
		Text: body.Text, Priority: feedback.Result, Voice: voice,
	})
	respond(writer, http.StatusOK, map[string]any{"ok": true})
}

// -- configuration ------------------------------------------------------------

func (s *Server) handleConfig(writer http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodGet:
		payload := s.engine.Config().ToMap()
		payload["_languages"] = i18n.Available()
		respond(writer, http.StatusOK, payload)
	case http.MethodPut:
		s.putConfig(writer, request)
	default:
		fail(writer, http.StatusMethodNotAllowed, "use GET or PUT for this")
	}
}

// putConfig merges a partial config, applies it live, and only then persists
// it. Applying before saving means a rejected change never reaches disk.
func (s *Server) putConfig(writer http.ResponseWriter, request *http.Request) {
	var body map[string]any
	if !decode(writer, request, &body) {
		return
	}
	delete(body, "_languages")

	merged := s.engine.Config().ToMap()
	for section, values := range body {
		existing, ok := merged[section].(map[string]any)
		if !ok {
			continue // an unknown section is ignored, not fatal
		}
		incoming, ok := values.(map[string]any)
		if !ok {
			continue
		}
		for key, value := range incoming {
			existing[key] = value
		}
		merged[section] = existing
	}

	updated := config.FromMap(merged)
	if err := s.engine.ApplyConfig(updated); err != nil {
		fail(writer, http.StatusBadRequest, "invalid configuration: "+err.Error())
		return
	}
	if _, err := updated.Save(""); err != nil {
		fail(writer, http.StatusInternalServerError, "could not save the configuration: "+err.Error())
		return
	}
	respond(writer, http.StatusOK, map[string]any{"ok": true, "config": updated.ToMap()})
}

func (s *Server) putSecret(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}
	if !decode(writer, request, &body) {
		return
	}
	if body.Name == "" {
		fail(writer, http.StatusBadRequest, "a secret needs a name")
		return
	}
	// The YouTube key is the one secret that changes what MikkiLens can do the
	// moment it is saved, so it takes effect now rather than at the next start.
	if body.Name != "" && body.Name == s.engine.Config().YouTube.APIKeyEnv {
		defer func() { go s.engine.RefreshYouTube(context.Background()) }()
	}

	if err := config.StoreSecret(body.Name, body.Value); err != nil {
		fail(writer, http.StatusInternalServerError, err.Error())
		return
	}
	respond(writer, http.StatusOK, map[string]any{"ok": true})
}

// -- commands -----------------------------------------------------------------

func (s *Server) handleCommands(writer http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodGet:
		s.getCommands(writer)
	case http.MethodPut:
		s.putCommands(writer, request)
	default:
		fail(writer, http.StatusMethodNotAllowed, "use GET or PUT for this")
	}
}

func (s *Server) getCommands(writer http.ResponseWriter) {
	set := s.engine.Commands()
	language := s.engine.Config().Language.Output

	unhandled := map[string]bool{}
	for _, id := range s.engine.Router().UnhandledCommands() {
		unhandled[id] = true
	}

	described := map[string]any{}
	for _, id := range set.Order {
		command := set.Commands[id]
		described[id] = map[string]any{
			"phrases":        set.PhrasesFor(id),
			"confirm":        command.Confirm,
			"confirm_prompt": command.ConfirmPrompt,
			"handled":        !unhandled[id],
		}
	}

	respond(writer, http.StatusOK, map[string]any{
		"language": language,
		"path":     paths.CommandsFile(language),
		"order":    orEmpty(set.Order),
		"warnings": orEmpty(set.Warnings),
		"commands": described,
	})
}

// putCommands validates, then writes. A rejected file never reaches disk:
// saving a broken command file would leave her with no working voice commands
// and nothing on screen to explain why.
func (s *Server) putCommands(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Commands map[string]struct {
			Phrases       []string `json:"phrases"`
			Confirm       bool     `json:"confirm"`
			ConfirmPrompt string   `json:"confirm_prompt"`
		} `json:"commands"`
	}
	if !decode(writer, request, &body) {
		return
	}

	document := map[string]any{}
	for id, spec := range body.Commands {
		phrases := []any{}
		for _, phrase := range spec.Phrases {
			if strings.TrimSpace(phrase) != "" {
				phrases = append(phrases, phrase)
			}
		}
		entry := map[string]any{"phrases": phrases, "confirm": spec.Confirm}
		if spec.ConfirmPrompt != "" {
			entry["confirm_prompt"] = spec.ConfirmPrompt
		}
		document[id] = entry
	}

	candidate, err := intent.SetFromMap(map[string]any{"commands": document}, "")
	if err != nil {
		fail(writer, http.StatusBadRequest, err.Error())
		return
	}

	encoded, err := toml.Marshal(map[string]any{"commands": document})
	if err != nil {
		fail(writer, http.StatusInternalServerError, err.Error())
		return
	}

	path := paths.CommandsFile(s.engine.Config().Language.Output)
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, append([]byte(commandsHeader), encoded...), 0o644); err != nil {
		fail(writer, http.StatusInternalServerError, err.Error())
		return
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		fail(writer, http.StatusInternalServerError, err.Error())
		return
	}

	s.engine.AdoptCommands(candidate)
	respond(writer, http.StatusOK, map[string]any{
		"ok": true, "count": candidate.Len(), "warnings": orEmpty(candidate.Warnings),
	})
}

func (s *Server) reloadCommands(writer http.ResponseWriter, _ *http.Request) {
	s.engine.ReloadCommands()
	set := s.engine.Commands()
	respond(writer, http.StatusOK, map[string]any{
		"ok": true, "count": set.Len(), "warnings": orEmpty(set.Warnings),
	})
}

// -- connection tests ---------------------------------------------------------

func (s *Server) testOBS(writer http.ResponseWriter, _ *http.Request) {
	controller := s.engine.OBS()
	if controller == nil {
		fail(writer, http.StatusBadRequest, "OBS is disabled in the settings")
		return
	}

	controller.Disconnect()
	if err := controller.Connect(); err != nil {
		respond(writer, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	result := map[string]any{"ok": true}
	if scenes, err := controller.Scenes(); err == nil {
		result["scenes"] = scenes
	}
	if scene, err := controller.CurrentScene(); err == nil {
		result["current_scene"] = scene
	}
	if streaming, err := controller.IsStreaming(); err == nil {
		result["streaming"] = streaming
	}
	respond(writer, http.StatusOK, result)
}

func (s *Server) testVision(writer http.ResponseWriter, request *http.Request) {
	ctx, cancel := context.WithTimeout(request.Context(), 60*time.Second)
	defer cancel()
	respond(writer, http.StatusOK,
		vision.New(s.engine.Config(), s.engine.Locale()).SelfTest(ctx))
}

// -- youtube ------------------------------------------------------------------

func (s *Server) youtubeStatus(writer http.ResponseWriter, _ *http.Request) {
	controller := s.engine.YouTube()
	if controller == nil {
		respond(writer, http.StatusOK, map[string]any{"enabled": false, "connected": false})
		return
	}

	transport := ""
	if ingest := s.engine.ChatIngest(); ingest != nil {
		transport = ingest.TransportInUse()
	}
	settings := s.engine.Config().YouTube
	respond(writer, http.StatusOK, map[string]any{
		"enabled": true,
		// "connected" keeps meaning what it always meant -- signed in -- so
		// nothing that already reads it starts reporting a key as a full
		// account. "access" is the finer answer.
		"connected":         controller.Authenticated(),
		"access":            string(controller.Access()),
		"has_api_key":       s.engine.Config().YouTubeAPIKey() != "",
		"channel_id":        settings.ChannelID,
		"video_id":          settings.VideoID,
		"api_key_env":       settings.APIKeyEnv,
		"has_client_secret": youtubeHasClientSecret(),
		// "file" means data/client_secret.json holds an OAuth client and the
		// sign-in button will work; "none" means signing in is not on offer,
		// and an API key is the way in.
		"client_source":  youtubeClientSource(),
		"channel":        controller.ChannelTitle(),
		"quota_used":     controller.Quota.Used(),
		"quota_budget":   controller.Quota.Budget(),
		"quota_percent":  controller.Quota.Percent(),
		"chat_transport": transport,
	})
}

// youtubeConnect runs the consent flow.
//
// It is a button rather than a voice command because the consent screen is the
// one step that genuinely needs sighted or screen-reader help, and it must
// never open unasked in the middle of a stream.
func (s *Server) youtubeConnect(writer http.ResponseWriter, request *http.Request) {
	controller := s.engine.YouTube()
	if controller == nil {
		fail(writer, http.StatusBadRequest, "YouTube is disabled in the settings")
		return
	}
	if !youtubeHasClientSecret() {
		fail(writer, http.StatusBadRequest,
			"Missing data/client_secret.json. Create OAuth desktop credentials "+
				"in Google Cloud and save the file there.")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	if err := controller.Authorize(ctx, openBrowser); err != nil {
		respond(writer, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	s.engine.OnYouTubeConnected(ctx)
	respond(writer, http.StatusOK, map[string]any{
		"ok": true, "channel": controller.ChannelTitle(),
	})
}

func (s *Server) youtubeDisconnect(writer http.ResponseWriter, _ *http.Request) {
	if controller := s.engine.YouTube(); controller != nil {
		controller.SignOut()
		s.engine.OnYouTubeDisconnected()
	}
	respond(writer, http.StatusOK, map[string]any{"ok": true})
}

// -- startup ------------------------------------------------------------------

func (s *Server) handleStartup(writer http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodGet:
		respond(writer, http.StatusOK, map[string]any{"enabled": StartupEnabled()})
	case http.MethodPut:
		var body struct {
			Enabled bool `json:"enabled"`
		}
		if !decode(writer, request, &body) {
			return
		}
		if err := SetStartup(body.Enabled); err != nil {
			fail(writer, http.StatusInternalServerError, err.Error())
			return
		}
		respond(writer, http.StatusOK, map[string]any{"ok": true, "enabled": StartupEnabled()})
	default:
		fail(writer, http.StatusMethodNotAllowed, "use GET or PUT for this")
	}
}

// -- actions ------------------------------------------------------------------

// runCommand runs one command by id, exactly as speaking it would.
//
// This is the endpoint for everything that is not a voice and not a key: a
// Stream Deck action that opens a URL, a companion app, a phone on the same
// network with `[ui] lan_access` on, or `mikkilensd do` from a shortcut.
//
// It answers as soon as the command has been started, not when it has
// finished. What happened is reported the way everything else is -- out loud --
// because the caller is a button on a desk, and nobody is watching for an HTTP
// status code.
func (s *Server) runCommand(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Command string `json:"command"`
		// Confirm is optional: absent leaves the command's own gate alone, so
		// a stop-the-stream button still asks unless it says not to.
		Confirm *bool `json:"confirm"`
	}
	if !decode(writer, request, &body) {
		return
	}

	id := strings.TrimSpace(body.Command)
	if id == "" {
		fail(writer, http.StatusBadRequest, "a command needs a name")
		return
	}
	// Checked here rather than left to the engine so a mistyped binding fails
	// where whoever is setting it up can see it, instead of only being spoken
	// into an empty room.
	if _, known := s.engine.Commands().Commands[id]; !known {
		fail(writer, http.StatusNotFound, "there is no command called "+id)
		return
	}

	confirm := true
	if body.Confirm != nil {
		confirm = *body.Confirm
	}
	s.engine.RunCommand(id, confirm)
	respond(writer, http.StatusOK, map[string]any{"ok": true, "command": id, "confirm": confirm})
}

func (s *Server) triggerListen(writer http.ResponseWriter, _ *http.Request) {
	s.engine.BeginListening()
	respond(writer, http.StatusOK, map[string]any{"ok": true})
}

// orEmpty keeps a nil slice from reaching the settings app as JSON null.
//
// "No warnings" is an empty list, not the absence of a list, and a page that
// counts them should not have to defend against the difference.
func orEmpty(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}
