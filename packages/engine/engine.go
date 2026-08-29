// Package engine is the running application: it wires audio, recognition and
// dispatch together.
//
// Ownership is deliberately linear -- one microphone stream, one speech bus,
// one router -- so there is exactly one place to look when something stops
// working.
//
// Two things happen off the main goroutine. Recognition loads in the
// background, because blocking startup on a model download would leave her
// listening to silence with nothing to explain it; and each command runs on
// its own goroutine so the hotkey and the audio callbacks stay responsive.
package engine

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/exzork/mikkilens/packages/audio/capture"
	"github.com/exzork/mikkilens/packages/audio/devices"
	"github.com/exzork/mikkilens/packages/audio/feedback"
	"github.com/exzork/mikkilens/packages/audio/hotkey"
	"github.com/exzork/mikkilens/packages/audio/stt"
	"github.com/exzork/mikkilens/packages/audio/tts"
	"github.com/exzork/mikkilens/packages/audio/wake"
	"github.com/exzork/mikkilens/packages/chat"
	"github.com/exzork/mikkilens/packages/controllers/obs"
	"github.com/exzork/mikkilens/packages/controllers/youtube"
	"github.com/exzork/mikkilens/packages/core/config"
	"github.com/exzork/mikkilens/packages/core/i18n"
	"github.com/exzork/mikkilens/packages/core/intent"
	"github.com/exzork/mikkilens/packages/core/paths"
	"github.com/exzork/mikkilens/packages/core/state"
)

// Engine is one running MikkiLens.
type Engine struct {
	mu       sync.RWMutex
	settings config.Config
	locale   *i18n.Locale

	store       *state.Store
	bus         *feedback.Bus
	commands    *intent.Set
	router      *intent.Router
	transcriber *stt.Transcriber

	microphone *capture.Stream
	removeWake func()
	wake       *wake.Detector
	hotkey     hotkey.Watcher
	obs        *obs.Controller
	obsSeen    bool
	youtube    *youtube.Controller
	ingest     *chat.Ingest
	reader     *chat.Reader

	listening  sync.Mutex
	listenBusy bool
	release    chan struct{}
	stopping   bool

	// OpenBrowser is how the engine opens a URL. The desktop app replaces it so
	// the OAuth consent screen lands in her real browser rather than nowhere.
	OpenBrowser func(url string)
}

// New builds an engine. Nothing is started until Start is called.
func New(settings config.Config, locale *i18n.Locale) *Engine {
	store := state.New()
	bus := feedback.New(settings, locale, outputDevice(settings))

	engine := &Engine{
		settings:    settings,
		locale:      locale,
		store:       store,
		bus:         bus,
		transcriber: stt.New(settings.STT, settings.Language.STT),
		release:     make(chan struct{}),
	}
	engine.commands = engine.loadCommands()
	engine.router = intent.NewRouter(engine.commands, bus, locale,
		time.Duration(settings.Speech.ConfirmTimeoutS*float64(time.Second)))

	engine.registerBuiltinHandlers()
	return engine
}

func outputDevice(settings config.Config) *devices.Device {
	device, err := devices.Resolve(settings.Speech.OutputDevice, devices.Output)
	if err != nil {
		slog.Error("no output device available; MikkiLens will not be able to speak", "error", err)
		return nil
	}
	return device
}

// -- accessors ----------------------------------------------------------------

func (e *Engine) Config() config.Config {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.settings
}

func (e *Engine) Locale() *i18n.Locale {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.locale
}

func (e *Engine) State() *state.Store           { return e.store }
func (e *Engine) Bus() *feedback.Bus            { return e.bus }
func (e *Engine) Router() *intent.Router        { return e.router }
func (e *Engine) Transcriber() *stt.Transcriber { return e.transcriber }

func (e *Engine) Commands() *intent.Set {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.commands
}

func (e *Engine) Wake() *wake.Detector {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.wake
}

func (e *Engine) Hotkey() hotkey.Watcher {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.hotkey
}

func (e *Engine) OBS() *obs.Controller {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.obs
}

func (e *Engine) YouTube() *youtube.Controller {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.youtube
}

func (e *Engine) ChatIngest() *chat.Ingest {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.ingest
}

func (e *Engine) ChatReader() *chat.Reader {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.reader
}

func (e *Engine) Microphone() *capture.Stream {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.microphone
}

// -- commands -----------------------------------------------------------------

func (e *Engine) loadCommands() *intent.Set {
	path := paths.CommandsFile(e.settings.Language.Output)
	set, err := intent.SetFromFile(path)
	if err != nil {
		slog.Error("could not load the command file", "path", path, "error", err)
		e.bus.SayKey("commands.reload_failed", feedback.Error, i18n.Args{"reason": err.Error()})
		// An empty set still starts: she can fix the file and say "reload".
		return &intent.Set{
			Commands: map[string]intent.Command{},
			Warnings: []string{err.Error()},
		}
	}
	return set
}

// ReloadCommands re-reads the command file without a restart, which matters
// because restarting mid-stream loses the OBS connection and the chat backlog.
func (e *Engine) ReloadCommands() {
	settings := e.Config()
	path := paths.CommandsFile(settings.Language.Output)

	set, err := intent.SetFromFile(path)
	if err != nil {
		e.bus.SayKey("commands.reload_failed", feedback.Error, i18n.Args{"reason": err.Error()})
		return
	}
	e.AdoptCommands(set)
	e.bus.SayKey("commands.reloaded", feedback.Result, i18n.Args{"count": set.Len()})
	e.announceCommandWarnings()
}

// AdoptCommands swaps in a new command set, keeping the handlers already wired.
func (e *Engine) AdoptCommands(set *intent.Set) {
	e.mu.Lock()
	e.commands = set
	e.mu.Unlock()
	e.router.SetCommands(set)
}

// announceCommandWarnings speaks the first problem in the command file. An
// ambiguous phrase is a command she will simply find does not work, with
// nothing on screen to explain why.
func (e *Engine) announceCommandWarnings() {
	warnings := e.Commands().Warnings
	for _, warning := range warnings {
		slog.Warn("command file", "problem", warning)
	}
	if len(warnings) > 0 {
		e.bus.SayKey("listen.ambiguous", feedback.Error, i18n.Args{"phrase": warnings[0]})
	}
}

// -- built-in handlers --------------------------------------------------------

func (e *Engine) registerBuiltinHandlers() {
	e.router.RegisterAll(map[string]intent.Handler{
		"help":            e.handleHelp,
		"reload_commands": func(map[string]string) error { e.ReloadCommands(); return nil },
	})
	// Registered up front, before the services connect, so an unavailable
	// feature says "YouTube is not connected" rather than the much less useful
	// "that command does not exist".
	e.router.RegisterAll(youTubeHandlers(e))
	e.router.RegisterAll(chatHandlers(e))
	e.router.RegisterAll(visionHandlers(e))
	e.router.RegisterAll(obsHandlers(e))
}

func (e *Engine) handleHelp(map[string]string) error {
	locale := e.Locale()
	e.bus.Say(locale.T("commands.help_intro"), feedback.Result)
	for _, phrase := range e.Commands().SpokenPhrases() {
		e.bus.Say(locale.T("commands.help_item", i18n.Args{"phrase": phrase}), feedback.Result)
	}
	return nil
}

// -- lifecycle ----------------------------------------------------------------

// Start brings everything up and says so when it is ready.
func (e *Engine) Start(ctx context.Context) {
	e.bus.Start()
	e.bus.SayKey("app.starting", feedback.Result)
	e.announceCommandWarnings()

	// Loading recognition can take a while the first time, so it happens in
	// parallel with bringing the microphone up: she hears "ready" as soon as
	// there is something to be ready about.
	loaded := make(chan struct{})
	go func() {
		defer close(loaded)
		e.loadRecognition(ctx)
	}()

	e.startMicrophone()
	e.startWakeWord()
	e.startHotkey()
	e.startOBS()
	e.startYouTube(ctx)

	select {
	case <-loaded:
	case <-time.After(3 * time.Minute):
		slog.Warn("speech recognition is still loading")
	case <-ctx.Done():
	}
	e.bus.SayKey("app.ready", feedback.Result)
}

func (e *Engine) loadRecognition(ctx context.Context) {
	if err := e.transcriber.Load(ctx); err != nil {
		slog.Error("could not load speech recognition", "error", err)
		e.bus.SayKey("error.generic", feedback.Error, i18n.Args{"reason": err.Error()})
		return
	}
	slog.Info("speech recognition ready", "backend", e.transcriber.Describe())
}

func (e *Engine) startMicrophone() {
	settings := e.Config()
	device, err := devices.Resolve(settings.Audio.InputDevice, devices.Input)
	if err != nil {
		slog.Warn("could not resolve the microphone", "error", err)
	}

	stream := capture.NewStream(device)
	if err := stream.Start(); err != nil {
		slog.Error("could not open the microphone", "error", err)
		e.bus.SayKey("error.mic_lost", feedback.Error)
		return
	}

	e.mu.Lock()
	e.microphone = stream
	e.mu.Unlock()
}

func (e *Engine) startWakeWord() {
	settings := e.Config()
	microphone := e.Microphone()
	if !settings.Wake.Enabled || microphone == nil {
		return
	}

	detector := wake.New(wake.Options{
		Model:      settings.Wake.Model,
		Threshold:  settings.Wake.Threshold,
		CooldownS:  settings.Wake.CooldownS,
		OnDetected: e.onWakeWord,
	})
	if err := detector.Load(); err != nil {
		// Not fatal: the hotkey is the reliable trigger anyway. But it is said
		// aloud, because a wake word that silently does nothing is worse than
		// one she knows is off.
		slog.Error("could not load the wake word", "error", err)
		e.bus.SayKey("error.generic", feedback.Error, i18n.Args{"reason": err.Error()})
		return
	}

	e.mu.Lock()
	e.wake = detector
	e.removeWake = microphone.AddListener(detector.Feed)
	e.mu.Unlock()
}

func (e *Engine) startHotkey() {
	settings := e.Config()
	if !settings.Hotkey.Enabled {
		return
	}

	watcher, err := hotkey.New(hotkey.Options{
		Combination: settings.Hotkey.Combination,
		PushToTalk:  settings.Hotkey.PushToTalk,
		OnActivate:  e.onHotkeyPress,
		OnRelease:   e.onHotkeyRelease,
	})
	if err == nil {
		err = watcher.Start()
	}
	if err != nil {
		slog.Error("could not set up the hotkey", "error", err)
		e.bus.SayKey("error.generic", feedback.Error, i18n.Args{"reason": err.Error()})
		return
	}

	e.mu.Lock()
	e.hotkey = watcher
	e.mu.Unlock()
}

func (e *Engine) startOBS() {
	settings := e.Config()
	if !settings.OBS.AutoConnect {
		return
	}

	controller := obs.New(obs.Options{
		Host:           settings.OBS.Host,
		Port:           settings.OBS.Port,
		Password:       settings.OBS.Password,
		MicSource:      settings.OBS.MicSource,
		ReconnectMaxS:  settings.OBS.ReconnectMaxS,
		OnConnected:    e.onOBSConnected,
		OnDisconnected: e.onOBSDisconnected,
		OnEvent:        e.onOBSEvent,
	})

	e.mu.Lock()
	e.obs = controller
	e.mu.Unlock()

	// The reconnect loop makes the first attempt too, so a closed OBS simply
	// connects later rather than needing MikkiLens to be restarted.
	controller.Start()
}

func (e *Engine) startYouTube(ctx context.Context) {
	settings := e.Config()
	if !settings.YouTube.Enabled {
		return
	}

	controller := youtube.New(settings.YouTube.QuotaBudget, settings.YouTube.QuotaWarnPercent)
	e.mu.Lock()
	e.youtube = controller
	e.mu.Unlock()

	// Only the cached token is used here. The consent screen is a browser flow
	// and must never appear unasked in the middle of a stream.
	connected, err := controller.LoadSavedCredentials(ctx)
	if err != nil {
		slog.Warn("could not restore the YouTube session", "error", err)
	}
	if !connected {
		e.store.Update(state.Changes{"youtube": state.Disconnected})
		slog.Info("YouTube is not connected; open the settings app to sign in")
		return
	}

	e.store.Update(state.Changes{"youtube": state.Connected})
	e.bus.SayKey("youtube.connected", feedback.Result,
		i18n.Args{"channel": e.channelName(ctx)})
	e.startChat()
}

func (e *Engine) channelName(ctx context.Context) string {
	controller := e.YouTube()
	if controller == nil {
		return "YouTube"
	}
	name, err := controller.ChannelName(ctx)
	if err != nil || name == "" {
		return "YouTube"
	}
	return name
}

// OnYouTubeConnected is called by the settings app once consent finishes.
func (e *Engine) OnYouTubeConnected(ctx context.Context) {
	e.store.Update(state.Changes{"youtube": state.Connected})
	e.bus.SayKey("youtube.connected", feedback.Result,
		i18n.Args{"channel": e.channelName(ctx)})
	if e.ChatIngest() == nil {
		e.startChat()
	}
}

// OnYouTubeDisconnected tears chat down when she signs out.
func (e *Engine) OnYouTubeDisconnected() {
	e.mu.Lock()
	reader, ingest := e.reader, e.ingest
	e.reader, e.ingest = nil, nil
	e.mu.Unlock()

	if reader != nil {
		reader.Stop()
	}
	if ingest != nil {
		ingest.Stop()
	}
	e.store.Update(state.Changes{"youtube": state.Disconnected, "chat": state.Unknown})
}

func (e *Engine) startChat() {
	settings := e.Config()
	controller := e.YouTube()
	if !settings.Chat.Enabled || controller == nil {
		return
	}

	ingest := chat.NewIngest(controller, chat.IngestOptions{
		Transport:      settings.YouTube.Transport,
		OnStatus:       e.onChatStatus,
		OnQuotaWarning: e.onQuotaWarning,
	})
	reader := chat.NewReader(ingest, e.bus, e.Locale(), settings.Chat, func(count int) {
		e.store.Update(state.Changes{"chat_backlog": count})
	})
	ingest.SetOnMessage(func(chat.Message) { reader.Notify() })

	e.mu.Lock()
	e.ingest, e.reader = ingest, reader
	e.mu.Unlock()

	ingest.Start()
	reader.Start(settings.Chat.AutostartReading)

	reading := state.ChatPaused
	if settings.Chat.AutostartReading {
		reading = state.ChatPlaying
	}
	e.store.Update(state.Changes{"chat_reading": reading})
}

func (e *Engine) onChatStatus(status, detail string) {
	switch status {
	case "connected":
		if e.store.Get("chat") != state.Connected {
			e.store.Update(state.Changes{"chat": state.Connected})
			e.bus.SayKey("chat.connected", feedback.Result)
		}
	case "quota":
		e.store.Update(state.Changes{"chat": state.Errored})
	case "disconnected":
		if e.store.Get("chat") == state.Connected {
			e.store.Update(state.Changes{"chat": state.Disconnected})
			e.bus.SayKey("chat.disconnected", feedback.Error)
		}
	}
	slog.Debug("chat status", "status", status, "detail", detail)
}

func (e *Engine) onQuotaWarning(percent int) {
	e.bus.SayKey("chat.quota_warning", feedback.Error, i18n.Args{"percent": percent})
}

// -- OBS events ---------------------------------------------------------------

func (e *Engine) onOBSConnected() {
	e.mu.Lock()
	seen := e.obsSeen
	e.obsSeen = true
	e.mu.Unlock()

	e.store.Update(state.Changes{"obs": state.Connected})
	if seen {
		e.bus.SayKey("obs.reconnected", feedback.Result)
	} else {
		e.bus.SayKey("obs.connected", feedback.Result)
	}

	// Syncing the initial state is best-effort: a failure here is not worth
	// announcing, because the connection itself already was.
	controller := e.OBS()
	if controller == nil {
		return
	}
	changes := state.Changes{}
	if scenes, err := controller.Scenes(); err == nil {
		changes["scenes"] = scenes
	}
	if scene, err := controller.CurrentScene(); err == nil {
		changes["current_scene"] = scene
	}
	if streaming, err := controller.IsStreaming(); err == nil {
		changes["streaming"] = streaming
	}
	if len(changes) > 0 {
		e.store.Update(changes)
	}
}

func (e *Engine) onOBSDisconnected(reason string) {
	slog.Warn("OBS disconnected", "reason", reason)
	e.store.Update(state.Changes{"obs": state.Disconnected})
	e.bus.SayKey("obs.disconnected", feedback.Error)
}

// onOBSEvent keeps the state in step with changes she made in OBS directly.
func (e *Engine) onOBSEvent(event obs.Event) {
	switch event.Kind {
	case "scene_changed":
		e.store.Update(state.Changes{"current_scene": event.SceneName})
	case "stream_state":
		e.store.Update(state.Changes{"streaming": event.Active})
	case "mute_changed":
		controller := e.OBS()
		if controller == nil {
			return
		}
		if name, err := controller.MicSourceName(); err == nil && name == event.InputName {
			e.store.Update(state.Changes{"mic_muted": event.Muted})
		}
	}
}

// Stop shuts everything down, saying goodbye first.
func (e *Engine) Stop() {
	e.mu.Lock()
	e.stopping = true
	reader, ingest := e.reader, e.ingest
	controller, watcher := e.obs, e.hotkey
	removeWake, detector := e.removeWake, e.wake
	microphone := e.microphone
	e.mu.Unlock()

	if reader != nil {
		reader.Stop()
	}
	if ingest != nil {
		ingest.Stop()
	}
	if controller != nil {
		controller.Stop()
	}
	if watcher != nil {
		watcher.Stop()
	}
	if removeWake != nil {
		removeWake()
	}
	if detector != nil {
		detector.Close()
	}
	if microphone != nil {
		microphone.Stop()
	}

	e.bus.SayKey("app.shutdown", feedback.Result)
	e.bus.WaitUntilIdle(10 * time.Second)
	e.bus.Stop()
}

// -- listening ----------------------------------------------------------------

func (e *Engine) onWakeWord(name string, score float64) {
	slog.Info("wake word detected", "model", name, "score", score)
	e.BeginListening()
}

func (e *Engine) onHotkeyPress() {
	e.mu.Lock()
	e.release = make(chan struct{})
	e.mu.Unlock()
	e.BeginListening()
}

// onHotkeyRelease ends the utterance without waiting for a pause, which is
// what makes hold-to-talk feel immediate.
func (e *Engine) onHotkeyRelease() {
	e.mu.Lock()
	release := e.release
	e.mu.Unlock()

	if release != nil {
		select {
		case <-release:
		default:
			close(release)
		}
	}
}

// BeginListening starts one listen, transcribe and dispatch cycle, unless one
// is already running.
func (e *Engine) BeginListening() {
	e.mu.RLock()
	stopping, microphone := e.stopping, e.microphone
	e.mu.RUnlock()

	if stopping || microphone == nil {
		return
	}

	e.listening.Lock()
	if e.listenBusy {
		e.listening.Unlock()
		slog.Debug("already listening; ignoring the trigger")
		return
	}
	e.listenBusy = true
	e.listening.Unlock()

	go e.listenOnce()
}

func (e *Engine) listenOnce() {
	defer func() {
		e.listening.Lock()
		e.listenBusy = false
		e.listening.Unlock()

		e.store.Update(state.Changes{"listening": false})
		if detector := e.Wake(); detector != nil {
			detector.Resume()
		}
		if problem := recover(); problem != nil {
			slog.Error("listening panicked", "panic", problem)
		}
	}()

	settings := e.Config()

	// The tone is the acknowledgement: it lands immediately, about a second
	// before any spoken reply could.
	e.bus.Earcon("listening")
	e.store.Update(state.Changes{"listening": true})
	if detector := e.Wake(); detector != nil {
		detector.Pause()
	}

	microphone := e.Microphone()
	if microphone == nil {
		return
	}

	// Only the hotkey path can end an utterance early. A wake word has no
	// release, so it always ends on a pause.
	var release <-chan struct{}
	if settings.Hotkey.PushToTalk {
		e.mu.RLock()
		release = e.release
		e.mu.RUnlock()
	}

	utterance := capture.Record(microphone, capture.RecorderOptions{
		Aggressiveness: settings.Audio.VadAggressiveness,
		SilenceMS:      settings.Audio.SilenceMS,
		MaxSeconds:     settings.Audio.MaxUtteranceS,
		IncludePreroll: true,
	}, release)

	if utterance.IsEmpty() {
		e.bus.SayKey("listen.no_speech", feedback.Result)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	transcript, err := e.transcriber.Transcribe(ctx, utterance.Audio)
	if err != nil {
		slog.Error("could not transcribe", "error", err)
		e.bus.SayKey("error.generic", feedback.Error, i18n.Args{"reason": err.Error()})
		return
	}
	slog.Info("heard", "text", transcript.Text,
		"audio_s", utterance.Duration, "decode_s", transcript.Elapsed)
	e.store.Update(state.Changes{"last_transcript": transcript.Text})

	if command := e.router.HandleTranscript(transcript.Text); command != "" {
		e.store.Update(state.Changes{"last_command": command})
	}
}

// -- live settings ------------------------------------------------------------

// ApplyConfig applies settings changes without a restart, so nothing needs
// restarting mid-stream.
func (e *Engine) ApplyConfig(updated config.Config) error {
	e.mu.Lock()
	previous := e.settings
	e.settings = updated
	e.mu.Unlock()

	e.bus.SetConfig(updated)

	if updated.Language.Output != previous.Language.Output {
		locale := i18n.Load(updated.Language.Output)
		e.mu.Lock()
		e.locale = locale
		e.mu.Unlock()

		e.bus.SetLocale(locale)
		e.router.SetLocale(locale)
		if reader := e.ChatReader(); reader != nil {
			reader.SetLocale(locale)
		}
		e.AdoptCommands(e.loadCommands())
	}

	if updated.Speech.Voice != previous.Speech.Voice ||
		updated.Speech.Rate != previous.Speech.Rate ||
		updated.Speech.Volume != previous.Speech.Volume {
		// Cached audio was rendered with the previous voice.
		tts.ClearCache()
	}

	if updated.Speech.OutputDevice != previous.Speech.OutputDevice {
		e.bus.SetDevice(outputDevice(updated))
	}

	if updated.Language.STT != previous.Language.STT {
		e.transcriber.SetLanguage(updated.Language.STT)
	}
	if updated.STT != previous.STT {
		e.transcriber.SetConfig(updated.STT)
		e.transcriber.Unload()
		go e.loadRecognition(context.Background())
	}

	if updated.Audio.InputDevice != previous.Audio.InputDevice {
		e.restartMicrophone()
	}

	if controller := e.OBS(); controller != nil {
		if updated.OBS.Host != previous.OBS.Host ||
			updated.OBS.Port != previous.OBS.Port ||
			updated.OBS.Password != previous.OBS.Password {
			controller.SetEndpoint(updated.OBS.Host, updated.OBS.Port, updated.OBS.Password)
			controller.Disconnect() // the reconnect loop picks the new endpoint up
		}
		controller.SetMicSource(updated.OBS.MicSource)
	}

	if reader := e.ChatReader(); reader != nil {
		reader.SetConfig(updated.Chat)
	}
	e.router.SetConfirmTimeout(
		time.Duration(updated.Speech.ConfirmTimeoutS * float64(time.Second)))
	return nil
}

func (e *Engine) restartMicrophone() {
	e.mu.Lock()
	removeWake, microphone := e.removeWake, e.microphone
	e.removeWake, e.microphone = nil, nil
	e.mu.Unlock()

	if removeWake != nil {
		removeWake()
	}
	if microphone != nil {
		microphone.Stop()
	}

	e.startMicrophone()

	detector, stream := e.Wake(), e.Microphone()
	if detector != nil && stream != nil {
		e.mu.Lock()
		e.removeWake = stream.AddListener(detector.Feed)
		e.mu.Unlock()
	}
}

// newTranscriberFor builds a transcriber from settings alone, for the self
// test, which runs before there is an engine to ask.
func newTranscriberFor(settings config.Config) *stt.Transcriber {
	return stt.New(settings.STT, settings.Language.STT)
}

// HasRunBefore reports whether a config file already exists, which is how the
// command line knows to suggest running setup.
func HasRunBefore() bool {
	_, err := os.Stat(paths.ConfigFile())
	return err == nil
}

// StartMicrophone opens the microphone on its own, without the rest of the
// engine. The listen subcommand uses it to test that one layer in isolation.
func (e *Engine) StartMicrophone() { e.startMicrophone() }
