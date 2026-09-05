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
	"github.com/exzork/mikkilens/packages/audio/wake"
	"github.com/exzork/mikkilens/packages/controllers/music"
	"github.com/exzork/mikkilens/packages/controllers/vision"
	"github.com/exzork/mikkilens/packages/controllers/youtube"
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

	mux.HandleFunc("/api/wake", only(http.MethodGet, s.getWake))

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
	mux.HandleFunc("/api/test/model", only(http.MethodPost, s.testModel))

	mux.HandleFunc("/api/youtube/status", only(http.MethodGet, s.youtubeStatus))
	mux.HandleFunc("/api/youtube/connect", only(http.MethodPost, s.youtubeConnect))
	mux.HandleFunc("/api/youtube/disconnect", only(http.MethodPost, s.youtubeDisconnect))
	s.channelRoutes(mux)

	mux.HandleFunc("/api/startup", s.handleStartup)
	mux.HandleFunc("/api/listen", only(http.MethodPost, s.triggerListen))
	mux.HandleFunc("/api/command", only(http.MethodPost, s.runCommand))

	mux.HandleFunc("/api/music/search", only(http.MethodPost, s.searchMusic))
	mux.HandleFunc("/api/music/songs", only(http.MethodGet, s.getSongs))
	mux.HandleFunc("/api/music/play", only(http.MethodPost, s.playSong))
	mux.HandleFunc("/api/music/prompt", only(http.MethodGet, s.waitForTyping))
	mux.HandleFunc("/api/mute", s.handleMute)
}

// -- music --------------------------------------------------------------------
//
// The typing window is the only client of these, and it exists because the one
// thing here that is typed rather than spoken is a song name -- the thing
// speech recognition is worst at, and the thing that has to be exactly right
// for the search to find it.
//
// Every one of these speaks as well as answering. The window shows the results
// so a sighted person helping can see them, but the answer she gets is the
// spoken one, and it is said whether or not the window is still open.

// searchMusic looks up what she typed and answers with what was found.
//
// It waits for the search rather than answering straight away. The window has
// a list to render and a "searching" state to come out of, and a request that
// returned immediately would leave it with neither -- while the spoken results
// arrived from somewhere it knew nothing about.
func (s *Server) searchMusic(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Query string `json:"query"`
	}
	if !decode(writer, request, &body) {
		return
	}

	songs, err := s.engine.FindSongs(request.Context(), body.Query)
	if err != nil {
		// Already said aloud by the engine, in her language. This is for the
		// window, which needs to stop spinning and say why in writing.
		fail(writer, http.StatusBadGateway, err.Error())
		return
	}
	respond(writer, http.StatusOK, map[string]any{
		"query": strings.TrimSpace(body.Query),
		"songs": songsOrEmpty(songs),
	})
}

// promptWait is how long a waiting window is left hanging before it is told
// nothing happened. Short enough that a window closed without a word is
// noticed within half a minute; long enough that reconnecting is rare.
const promptWait = 25 * time.Second

// waitForTyping is where the desktop app parks until the box she types a song
// name into is asked for.
//
// A long poll rather than a socket, because this is the only thing the main
// process ever needs pushed to it and a WebSocket client for one message is a
// dependency to carry, a reconnect loop to get right and a failure mode that
// looks like the key silently not working.
//
// The count in and out is what makes a reconnect safe: a window says which
// request it last saw, and one that arrived while it was away is answered
// straight away rather than lost.
func (s *Server) waitForTyping(writer http.ResponseWriter, request *http.Request) {
	since, _ := strconv.Atoi(request.URL.Query().Get("since"))

	ctx, cancel := context.WithTimeout(request.Context(), promptWait)
	defer cancel()

	count := s.engine.WaitForTyping(ctx, since)
	respond(writer, http.StatusOK, map[string]any{
		"count": count,
		// Whether this answer is the box being asked for, or the wait simply
		// running out. The window opens on the first and asks again on the
		// second.
		"open": count > since,
	})
}

// getSongs is what can still be picked, so a window opened again can offer the
// last results rather than an empty box.
func (s *Server) getSongs(writer http.ResponseWriter, _ *http.Request) {
	respond(writer, http.StatusOK, map[string]any{"songs": songsOrEmpty(s.engine.Songs())})
}

// playSong starts the nth result, counting from one -- the same number she
// heard read out, so the key she presses and the number she would say are the
// same number.
func (s *Server) playSong(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Number int `json:"number"`
	}
	if !decode(writer, request, &body) {
		return
	}

	song, err := s.engine.PlaySong(body.Number)
	if err != nil {
		fail(writer, http.StatusBadRequest, err.Error())
		return
	}
	respond(writer, http.StatusOK, map[string]any{"ok": true, "song": song})
}

func songsOrEmpty(songs []music.Song) []music.Song {
	if songs == nil {
		return []music.Song{}
	}
	return songs
}

// -- the mute -----------------------------------------------------------------

// handleMute reads or sets whether chat is being read aloud.
//
// The key is the way she uses this; the route is for the tray menu and for
// whoever is helping her, who would rather press something than learn a
// keyboard shortcut on somebody else's machine.
func (s *Server) handleMute(writer http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodGet:
		respond(writer, http.StatusOK, map[string]any{"muted": s.engine.Bus().ChatMuted()})
	case http.MethodPost:
		var body struct {
			// A pointer, so "toggle" and "set it to false" are different
			// requests. The tray menu sets it; the key toggles it, and a key
			// that has to read the state first is a key that can be wrong.
			Muted *bool `json:"muted"`
		}
		if !decode(writer, request, &body) {
			return
		}
		if body.Muted == nil {
			s.engine.ToggleChatMute()
		} else {
			s.engine.SetChatMute(*body.Muted)
		}
		respond(writer, http.StatusOK, map[string]any{"muted": s.engine.Bus().ChatMuted()})
	default:
		fail(writer, http.StatusMethodNotAllowed, "use GET or POST for this")
	}
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

	// The first-run download, so the status page can show how far along it is.
	// It is announced aloud as well -- this is for the person helping her, who
	// is looking at the screen and wants to know whether it is moving.
	if progress := s.engine.Installing(); progress.Stage != "" {
		snapshot["installing"] = progress
	} else {
		snapshot["installing"] = nil
	}
	snapshot["command_count"] = s.engine.Commands().Len()
	snapshot["unhandled"] = orEmpty(s.engine.Router().UnhandledCommands())

	if detector := s.engine.Wake(); detector != nil {
		snapshot["wake_model"] = detector.ModelName()
		snapshot["wake_score"] = detector.LastScore()
	} else {
		snapshot["wake_model"] = nil
	}
	// Why it is off travels with the fact that it is off. "Wake word:
	// disabled" on its own is the reading she is trying to explain.
	snapshot["wake_error"] = s.engine.WakeError()

	if watcher := s.engine.Hotkey(); watcher != nil {
		snapshot["hotkey"] = watcher.Combination()
	} else {
		snapshot["hotkey"] = nil
	}
	snapshot["hotkey_error"] = s.engine.HotkeyError()

	if microphone := s.engine.Microphone(); microphone != nil {
		snapshot["mic_frames"] = microphone.FramesSeen()
		snapshot["mic_error"] = microphone.LastError()
	}
	return snapshot
}

// getWake is everything the settings page needs to make a wake word work:
// which ones are installed, which one is running, and what the microphone and
// the detector are hearing right now.
//
// The live numbers are polled rather than pushed over the socket. They are
// only interesting while she is looking at that one panel with her voice in
// the room, and a score that updates twelve times a second is not state worth
// waking every client for.
func (s *Server) getWake(writer http.ResponseWriter, _ *http.Request) {
	settings := s.engine.Config().Wake

	payload := map[string]any{
		"enabled":    settings.Enabled,
		"model":      settings.Model,
		"threshold":  settings.Threshold,
		"cooldown_s": settings.CooldownS,
		"installed":  orEmpty(wake.Installed()),
		"error":      s.engine.WakeError(),
		"loaded":     false,
		"score":      0.0,
		"mic_level":  0.0,
		"mic_frames": 0,
		"mic_error":  "",
	}
	if err := wake.RuntimeReady(); err != nil {
		payload["runtime_error"] = err.Error()
	}
	if detector := s.engine.Wake(); detector != nil {
		payload["loaded"] = detector.Loaded()
		payload["running_model"] = detector.ModelName()
		payload["score"] = detector.LastScore()
		payload["paused"] = !detector.Enabled()
	}
	if microphone := s.engine.Microphone(); microphone != nil {
		payload["mic_level"] = microphone.Level()
		payload["mic_frames"] = microphone.FramesSeen()
		payload["mic_error"] = microphone.LastError()
		payload["mic_running"] = microphone.Running()
	}
	respond(writer, http.StatusOK, payload)
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
		// A little above her own tone volume: this one is being listened for
		// on a device she is not sure is the right one.
		volume := float64(config.ClampPercent(s.engine.Config().Speech.EarconVolume+10)) / 100
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

// speak reads a line out through the queue. The optional priority is what a
// donation alert will post through: "donation" queues above the chat backlog
// and takes the donation voice, while anything else keeps the old behaviour of
// speaking at result priority in the main voice.
//
// Rate and volume are optional and are what the sample button on the settings
// page sends: the sliders as they stand, before Save. Hearing the saved volume
// instead of the one just dragged to is indistinguishable from the slider
// doing nothing.
func (s *Server) speak(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Text     string `json:"text"`
		Voice    string `json:"voice"`
		Priority string `json:"priority"`
		Rate     string `json:"rate"`
		Volume   *int   `json:"volume"`
	}
	if !decode(writer, request, &body) {
		return
	}
	if strings.TrimSpace(body.Text) == "" {
		fail(writer, http.StatusBadRequest, "there is nothing to say")
		return
	}

	settings := s.engine.Config()
	utterance := feedback.Utterance{Text: body.Text, Priority: feedback.Result, Voice: body.Voice}

	if strings.EqualFold(strings.TrimSpace(body.Priority), "donation") {
		utterance.Priority = feedback.Donation
		utterance.Earcon = "donation"
		utterance.Rate = settings.Speech.DonationRate
		utterance.Volume = feedback.At(settings.Speech.DonationVolume)
		utterance.RequeueIfInterrupted = true
		if utterance.Voice == "" {
			utterance.Voice = settings.VoiceForDonation(s.engine.Locale().DefaultVoice())
		}
	}
	if utterance.Voice == "" {
		utterance.Voice = settings.Voice(s.engine.Locale().DefaultVoice())
	}
	if body.Rate != "" {
		utterance.Rate = body.Rate
	}
	if body.Volume != nil {
		utterance.Volume = feedback.At(config.ClampPercent(*body.Volume))
	}

	s.engine.Bus().Enqueue(utterance)
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

// testModel checks the one configured provider, with an image, because the
// image is the part that fails quietly: a text-only model answers the chat
// summary perfectly well and then has nothing to say about her screen.
func (s *Server) testModel(writer http.ResponseWriter, request *http.Request) {
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
	respond(writer, http.StatusOK, map[string]any{
		"enabled":   true,
		"connected": controller.Authenticated(),
		"access":    string(controller.Access()),
		// Whether there is an OAuth client to sign in with at all. Without
		// one, Connect cannot work and the page should say why rather than
		// offering a button that fails.
		"has_client": youtube.HasClientSecret(),
		"channel":    controller.ChannelTitle(),

		"quota_used":     controller.Quota.Used(),
		"quota_budget":   controller.Quota.Budget(),
		"quota_percent":  controller.Quota.Percent(),
		"chat_transport": transport,
	})
}

// youtubeConnect runs the consent flow. It blocks while she is in the browser,
// which is why the request context is what gives up on it: closing the
// settings page is a way to cancel a sign-in she changed her mind about.
func (s *Server) youtubeConnect(writer http.ResponseWriter, request *http.Request) {
	if err := s.engine.ConnectYouTube(request.Context()); err != nil {
		fail(writer, http.StatusBadGateway, err.Error())
		return
	}
	respond(writer, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) youtubeDisconnect(writer http.ResponseWriter, _ *http.Request) {
	if err := s.engine.DisconnectYouTube(); err != nil {
		fail(writer, http.StatusInternalServerError, err.Error())
		return
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
