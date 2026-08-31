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
	"errors"
	"log/slog"
	"os"
	"strings"
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
	"github.com/exzork/mikkilens/packages/controllers/llm"
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
	bindings   []hotkey.Watcher
	obs        *obs.Controller
	obsSeen    bool
	youtube    *youtube.Controller
	ingest     *chat.Ingest
	reader     *chat.Reader

	listening  sync.Mutex
	listenBusy bool
	release    chan struct{}
	stopping   bool

	// matcherQuarter is how much of a model download has already been
	// announced, so progress is spoken at quarters rather than continuously.
	matcherQuarter int

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
	e.startBindings()
	e.startOBS()
	// Same guarantee at the other end: whatever a previous run cached is about
	// a broadcast that may well have finished while MikkiLens was closed.
	if controller := e.YouTube(); controller != nil {
		controller.InvalidateBroadcast()
	}
	devices.SetLeadIn(time.Duration(e.Config().Speech.LeadInMs) * time.Millisecond)
	e.applyUnderstander(e.Config())
	e.startBundledModel(e.Config())

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

	mode := hotkey.Toggle
	if settings.Hotkey.PushToTalk {
		mode = hotkey.Hold
	}
	watcher, err := hotkey.New(hotkey.Options{
		Combination: settings.Hotkey.Combination,
		Mode:        mode,
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

// startBindings gives every bound key its own watcher.
//
// Windows registers each combination separately, so a key that another
// application already owns fails on its own and takes none of the others with
// it. Each failure is spoken, because a key that silently does nothing is
// indistinguishable from a broken key, and the config file it came from is
// not something she can glance at.
func (e *Engine) startBindings() {
	for _, binding := range e.Config().Bindings {
		e.startBinding(binding)
	}
}

func (e *Engine) startBinding(binding config.Binding) {
	command := strings.TrimSpace(binding.Command)
	combination := strings.TrimSpace(binding.Combination)
	if command == "" || combination == "" {
		return
	}
	if _, known := e.Commands().Commands[command]; !known {
		slog.Error("a bound key names a command that does not exist",
			"combination", combination, "command", command)
		e.bus.SayKey("error.not_available", feedback.Error, i18n.Args{"command": command})
		return
	}

	// A binding may waive the command's own confirmation, but never add one.
	confirm := true
	if binding.Confirm != nil {
		confirm = *binding.Confirm
	}

	watcher, err := hotkey.New(hotkey.Options{
		Combination: combination,
		Mode:        hotkey.Press,
		OnActivate:  func() { e.RunCommand(command, confirm) },
	})
	if err == nil {
		err = watcher.Start()
	}
	if err != nil {
		slog.Error("could not bind a key",
			"combination", combination, "command", command, "error", err)
		e.bus.SayKey("error.generic", feedback.Error, i18n.Args{"reason": err.Error()})
		return
	}

	slog.Info("key bound", "combination", combination, "command", command, "confirm", confirm)
	e.mu.Lock()
	e.bindings = append(e.bindings, watcher)
	e.mu.Unlock()
}

// Bindings are the keys currently bound to commands.
func (e *Engine) Bindings() []hotkey.Watcher {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return append([]hotkey.Watcher(nil), e.bindings...)
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

	controller := youtube.New(settings.YouTube, settings.YouTubeAPIKey())
	e.mu.Lock()
	e.youtube = controller
	e.mu.Unlock()

	// Only the cached token is used here. The consent screen is a browser flow
	// and must never appear unasked in the middle of a stream.
	connected, err := controller.LoadSavedCredentials(ctx)
	if err != nil {
		// Having no credentials yet is where everyone starts, not a fault.
		// Reporting it as one at every launch teaches her to ignore the log,
		// which is the last thing this application can afford.
		var expired *youtube.ExpiredCredentialsError
		switch {
		case errors.As(err, &expired):
			// Silence here would be the worst outcome: she would find out by
			// asking for the viewer count mid-stream and being told YouTube is
			// not connected, with no idea why or what to do about it.
			slog.Warn("the YouTube sign-in has expired", "next", "connect again in the settings app")
			e.bus.SayKey("youtube.sign_in_expired", feedback.Error)
		case youtube.HasClientSecret():
			slog.Warn("could not restore the YouTube session", "error", err)
		case settings.YouTubeAPIKey() != "":
			// There is a key, which covers reading. Reporting the missing
			// sign-in as a problem here would be reporting a choice she made.
		default:
			slog.Info("YouTube is not set up yet",
				"next", "add data/client_secret.json, or just paste an API key "+
					"and your channel link in the settings app")
		}
	}

	if !connected {
		// No sign-in. An API key still answers the two questions asked most
		// often -- how many are watching, what is this stream called -- so
		// fall back to it rather than reporting YouTube as simply off.
		if err := controller.StartPublic(ctx); err != nil {
			slog.Warn("the YouTube API key could not be used", "error", err)
		}
	}

	switch controller.Access() {
	case youtube.AccessNone:
		e.store.Update(state.Changes{"youtube": state.Disconnected})
		return
	case youtube.AccessPublic:
		e.store.Update(state.Changes{"youtube": state.Connected})
		e.bus.SayKey("youtube.connected_public", feedback.Result, nil)
	default:
		e.store.Update(state.Changes{"youtube": state.Connected})
		e.bus.SayKey("youtube.connected", feedback.Result,
			i18n.Args{"channel": e.channelName(ctx)})
	}
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
	// A sign-in can be a different account entirely, so nothing learned before
	// it still applies.
	if controller := e.YouTube(); controller != nil {
		controller.InvalidateBroadcast()
	}

	e.store.Update(state.Changes{"youtube": state.Connected})
	e.bus.SayKey("youtube.connected", feedback.Result,
		i18n.Args{"channel": e.channelName(ctx)})
	if e.ChatIngest() == nil {
		e.startChat()
	}
}

// RefreshYouTube picks up a newly saved API key or channel without a restart.
//
// Telling someone to restart the application after saving a setting is a bad
// answer generally, and a worse one here: she cannot see whether the restart
// worked, so the next thing she hears has to be the answer either way.
//
// A live sign-in is left alone. It already does everything a key does and
// more, and rebuilding it would drop the chat connection to gain nothing.
func (e *Engine) RefreshYouTube(ctx context.Context) {
	controller := e.YouTube()
	if controller != nil && controller.Access() == youtube.AccessAccount {
		return
	}

	e.OnYouTubeDisconnected()
	e.startYouTube(ctx)
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
		// So that closing MikkiLens and opening it again does not read the
		// last few minutes of chat out a second time.
		ReadCursor: chat.LoadReadCursor(),
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
	case "unavailable":
		// Said once, on the way into the state, and not again while it lasts.
		// This is a condition that can persist for a whole stream, and it is
		// checked every couple of minutes in case she switches chat on --
		// announcing each of those checks would be unbearable.
		if e.store.Get("chat") != state.Errored {
			e.store.Update(state.Changes{"chat": state.Errored})
			e.bus.SayKey("chat.unavailable", feedback.Error)
		}
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
		// Going live mints a new YouTube broadcast, and ending the stream
		// finishes one. Either way whatever is cached is now about a different
		// broadcast than the one she is on, so it is dropped here rather than
		// waited out -- this is the moment a new broadcast gets its own live
		// chat, and the moment the old one stops having anything to read.
		if controller := e.YouTube(); controller != nil {
			controller.InvalidateBroadcast()
		}
		// And look again now. Dropping the cache alone would leave chat
		// waiting out a timer before noticing the stream she just started.
		if ingest := e.ChatIngest(); ingest != nil {
			ingest.Recheck()
		}
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
	// Nothing learned about the current broadcast should outlive this run. The
	// cache is in memory, so a normal restart starts clean regardless -- this
	// is for the paths that keep the controller and start the engine again,
	// where a broadcast from the previous run would otherwise survive.
	if controller := e.YouTube(); controller != nil {
		controller.InvalidateBroadcast()
	}
	// The model server is a child process: leaving it running would hold
	// gigabytes and a port after MikkiLens has gone.
	llm.Bundled().Stop()

	e.mu.Lock()
	e.stopping = true
	reader, ingest := e.reader, e.ingest
	controller, watcher := e.obs, e.hotkey
	bindings := e.bindings
	e.bindings = nil
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
	for _, bound := range bindings {
		bound.Stop()
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
	if _, ok := e.beginTurn(); !ok {
		return
	}
	go e.listenOnce()
}

// beginTurn reserves the microphone for one turn.
//
// There is one microphone and one voice, so there is one turn at a time --
// whether it was started by the hotkey, by the wake word, or by a bound key
// asking her a question it now needs answered.
func (e *Engine) beginTurn() (*capture.Stream, bool) {
	e.mu.RLock()
	stopping, microphone := e.stopping, e.microphone
	e.mu.RUnlock()

	if stopping || microphone == nil {
		return nil, false
	}

	e.listening.Lock()
	defer e.listening.Unlock()
	if e.listenBusy {
		slog.Debug("already listening; ignoring the trigger")
		return nil, false
	}
	e.listenBusy = true
	return microphone, true
}

// endTurn gives the microphone back. Every turn defers it.
func (e *Engine) endTurn() {
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
}

func (e *Engine) listenOnce() {
	defer e.endTurn()

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

	if !e.captureAndRoute(settings, microphone, release) {
		return
	}

	// A question deserves the microphone that asked it.
	//
	// "Stop the stream? Say yes or no" used to end the listening turn, so the
	// microphone went off and answering meant saying the wake word again
	// first -- which is not what anyone does when they have just been asked a
	// question. She would answer into a microphone that was not listening,
	// hear nothing back, and have no way to see why.
	e.answerConfirmation(settings, microphone)
}

// RunCommand runs a command that nobody said.
//
// A bound key, the local API and `mikkilensd do` all come through here, which
// is what lets a Stream Deck of any brand, a mouse macro or a foot pedal drive
// the same commands as her voice: from this line down there is no difference
// between a key press and a sentence, including everything that gets said back.
//
// It returns immediately. The work happens on its own goroutine because the
// caller is a key press being reported by the Windows message loop, and
// holding that up while OBS is talked to would stall every other key on the
// machine.
func (e *Engine) RunCommand(id string, confirm bool) {
	go e.runCommand(id, confirm)
}

func (e *Engine) runCommand(id string, confirm bool) {
	defer func() {
		if problem := recover(); problem != nil {
			slog.Error("a bound command panicked", "command", id, "panic", problem)
		}
	}()

	e.router.Trigger(id, confirm)
	if !e.router.AwaitingConfirmation() {
		return
	}

	// She has just been asked a question by a key press, so the microphone has
	// to open itself. There is no key being held to answer into, and telling
	// her to press something else to answer would be a worse question than the
	// one just asked.
	microphone, ok := e.beginTurn()
	if !ok {
		// The question cannot be answered, so it must not be left hanging:
		// silence after "stop the stream?" is the one ending this must never
		// have, because it reads exactly like the stream having stopped.
		e.router.CancelPending()
		return
	}
	defer e.endTurn()

	e.store.Update(state.Changes{"listening": true})
	if detector := e.Wake(); detector != nil {
		detector.Pause()
	}
	e.answerConfirmation(e.Config(), microphone)
}

// maxConfirmTurns bounds the follow-up. An answer that keeps not being
// understood must not become an unbroken loop of being asked again.
const maxConfirmTurns = 3

// answerConfirmation keeps listening while a confirmation is open.
func (e *Engine) answerConfirmation(settings config.Config, microphone *capture.Stream) {
	heard := true

	for turn := 0; e.router.AwaitingConfirmation() && turn < maxConfirmTurns; turn++ {
		// Wait for the question to finish being asked. Recording through it
		// would capture MikkiLens's own voice and try to hear "yes" in it.
		if !e.bus.WaitUntilIdle(confirmSpeechTimeout) {
			break
		}
		// The clock starts now, when the question has actually finished being
		// asked, rather than when it was queued -- otherwise a long prompt
		// eats the time she was meant to have to answer it.
		e.router.RenewPending()
		if !e.router.AwaitingConfirmation() {
			return
		}

		e.bus.Earcon("listening")

		// No release channel here: she is answering a question, not holding a
		// key down, so the answer ends on a pause like any other sentence.
		if heard = e.captureAndRoute(settings, microphone, nil); !heard {
			break
		}
	}

	// Still open means she never answered, or never answered clearly. Either
	// way the command did not run, and that has to be said rather than left
	// as silence she might read as "done".
	if e.router.AwaitingConfirmation() {
		if heard {
			e.router.CancelPending()
		} else {
			e.router.TimeOutPending()
		}
	}
}

// confirmSpeechTimeout caps how long to wait for a question to be spoken
// before giving up on hearing the answer.
const confirmSpeechTimeout = 20 * time.Second

// captureAndRoute records one utterance, recognizes it and routes it. It
// reports whether anything was actually heard.
func (e *Engine) captureAndRoute(
	settings config.Config, microphone *capture.Stream, release <-chan struct{},
) bool {
	utterance := capture.Record(microphone, capture.RecorderOptions{
		Aggressiveness: settings.Audio.VadAggressiveness,
		SilenceMS:      settings.Audio.SilenceMS,
		MaxSeconds:     settings.Audio.MaxUtteranceS,
		IncludePreroll: true,
	}, release)

	if utterance.IsEmpty() {
		e.bus.SayKey("listen.no_speech", feedback.Result)
		return false
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	transcript, err := e.transcriber.Transcribe(ctx, utterance.Audio)
	if err != nil {
		slog.Error("could not transcribe", "error", err)
		e.bus.SayKey("error.generic", feedback.Error, i18n.Args{"reason": err.Error()})
		return false
	}
	slog.Info("heard", "text", transcript.Text,
		"audio_s", utterance.Duration, "decode_s", transcript.Elapsed)
	e.store.Update(state.Changes{"last_transcript": transcript.Text})

	if command := e.router.HandleTranscript(transcript.Text); command != "" {
		e.store.Update(state.Changes{"last_command": command})
	}
	return true
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

	if updated.Speech.LeadInMs != previous.Speech.LeadInMs {
		devices.SetLeadIn(time.Duration(updated.Speech.LeadInMs) * time.Millisecond)
	}

	if updated.Matcher != previous.Matcher {
		e.applyUnderstander(updated)
		e.startBundledModel(updated)
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

	if updated.YouTube != previous.YouTube {
		go e.RefreshYouTube(context.Background())
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
