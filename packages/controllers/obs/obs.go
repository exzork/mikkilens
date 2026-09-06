// Package obs drives OBS Studio over its built-in WebSocket server.
//
// Scene and source names are matched fuzzily against what OBS actually has, so
// "ganti ke just chatting" finds a scene called "Just Chatting" without her
// having to pronounce capitalisation. The names come from OBS live rather than
// from config, so renaming a scene in OBS needs no change here.
//
// Connection loss is announced aloud and retried with backoff. A silent
// disconnect would look exactly like MikkiLens ignoring her.
package obs

import (
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/andreykaipov/goobs"
	"github.com/andreykaipov/goobs/api/events"
	"github.com/andreykaipov/goobs/api/requests/inputs"

	"github.com/exzork/mikkilens/packages/core/fuzzy"
)

// nameMatchThreshold is how close a spoken name has to be before it counts as
// referring to something that exists -- a channel's OBS profile, or its scene
// collection.
const nameMatchThreshold = 65.0

// responseTimeout is how long any one request may take.
//
// Generous, because SetCurrentSceneCollection blocks until OBS has torn down
// every source and built the next collection's, and on a real scene collection
// that is seconds rather than milliseconds. Timing that out would report a
// switch as failed while OBS goes on and completes it.
//
// It must never be set with goobs.WithResponseTimeout, which is deprecated and
// reads its time.Duration as milliseconds -- so the obvious 5*time.Second asked
// for about fifty-eight days, meaning nothing ever timed out and a stalled OBS
// hung the reconnect loop for good.
const responseTimeout = 30 * time.Second

// reloadPatience is how long a scene collection change may be in flight before
// requests are allowed again regardless.
//
// The pause is normally lifted by the "changed" event; this is the backstop for
// an event that never arrives. Without it one missed message would leave every
// later command answering "OBS is reloading" until she restarts.
const reloadPatience = 90 * time.Second

// outputCaptureKinds capture what comes *out* of a device. These are the ones
// that would put MikkiLens's own voice onto the broadcast.
var outputCaptureKinds = map[string]bool{
	"wasapi_output_capture":    true,
	"coreaudio_output_capture": true,
}

// inputCaptureKinds are microphones.
var inputCaptureKinds = map[string]bool{
	"wasapi_input_capture":    true,
	"coreaudio_input_capture": true,
	"dshow_input":             true,
}

// Error is an OBS failure worth reporting aloud.
type Error struct{ Reason string }

func (e *Error) Error() string { return e.Reason }

// NotConnectedError means OBS is not reachable right now.
type NotConnectedError struct{ Reason string }

func (e *NotConnectedError) Error() string { return e.Reason }

// ReloadingError means OBS is swapping scene collections and cannot be asked
// anything until it has finished. It clears itself; nothing has gone wrong.
type ReloadingError struct{ Reason string }

func (e *ReloadingError) Error() string { return e.Reason }

// StreamingError means the change would reconfigure an output OBS is using, so
// it has to wait until she is off air.
type StreamingError struct{ Reason string }

func (e *StreamingError) Error() string { return e.Reason }

// Event is a change she made in OBS directly, which the app mirrors so its
// state never drifts from what is actually on screen.
type Event struct {
	// Kind is one of "scene_changed", "stream_state", "mute_changed",
	// "profile_changed", "collection_reloading" or "collection_changed".
	Kind           string
	SceneName      string
	InputName      string
	ProfileName    string
	CollectionName string
	Muted          bool
	Active         bool
}

// Options configure the controller.
type Options struct {
	Host          string
	Port          int
	Password      string
	MicSource     string
	ReconnectMaxS float64

	OnConnected    func()
	OnDisconnected func(reason string)
	OnEvent        func(Event)
}

// Controller is a request client plus an event stream, with auto-reconnect.
type Controller struct {
	mu      sync.RWMutex
	options Options
	client  *goobs.Client

	stop         chan struct{}
	done         chan struct{}
	running      bool
	wasConnected bool
	lastError    string

	// reloadingSince is when a scene collection change started, and zero when
	// none is in flight. obs-websocket is explicit that a request made while a
	// collection is loading is undefined behaviour and can crash OBS, so every
	// request is refused for the duration rather than merely discouraged.
	reloadingSince time.Time
}

// New prepares a controller. Nothing connects until Start is called.
func New(options Options) *Controller {
	if options.ReconnectMaxS <= 0 {
		options.ReconnectMaxS = 30
	}
	return &Controller{options: options}
}

// Connected reports whether OBS is reachable right now.
func (c *Controller) Connected() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.client != nil
}

// LastError is the most recent failure, for the settings page.
func (c *Controller) LastError() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lastError
}

// SetEndpoint changes where to connect. The next reconnect uses it.
func (c *Controller) SetEndpoint(host string, port int, password string) {
	c.mu.Lock()
	c.options.Host, c.options.Port, c.options.Password = host, port, password
	c.mu.Unlock()
}

// SetMicSource changes which OBS input counts as her microphone.
func (c *Controller) SetMicSource(name string) {
	c.mu.Lock()
	c.options.MicSource = name
	c.mu.Unlock()
}

// Connect opens one connection. It returns an error if OBS is not reachable.
func (c *Controller) Connect() error {
	c.mu.Lock()
	if c.client != nil {
		c.mu.Unlock()
		return nil
	}
	address := fmt.Sprintf("%s:%d", c.options.Host, c.options.Port)
	password := c.options.Password
	c.mu.Unlock()

	client, err := goobs.New(address,
		goobs.WithPassword(password),
		goobs.WithResponseTimeoutDuration(responseTimeout))
	if err != nil {
		c.mu.Lock()
		c.lastError = err.Error()
		c.mu.Unlock()
		return &Error{Reason: err.Error()}
	}

	c.mu.Lock()
	c.client = client
	c.lastError = ""
	c.wasConnected = true
	onConnected := c.options.OnConnected
	c.mu.Unlock()

	go c.watchEvents(client)

	if onConnected != nil {
		onConnected()
	}
	return nil
}

// Disconnect closes the connection without stopping the reconnect loop.
func (c *Controller) Disconnect() {
	c.mu.Lock()
	client := c.client
	c.client = nil
	c.mu.Unlock()

	if client != nil {
		_ = client.Disconnect()
	}
}

// Start begins connecting, and keeps trying in the background.
//
// The loop makes the first attempt too, so OBS being closed at startup simply
// connects later rather than needing MikkiLens to be restarted.
func (c *Controller) Start() {
	c.mu.Lock()
	if c.running {
		c.mu.Unlock()
		return
	}
	c.running = true
	c.stop = make(chan struct{})
	c.done = make(chan struct{})
	stop, done := c.stop, c.done
	c.mu.Unlock()

	go c.reconnectLoop(stop, done)
}

// Stop ends the reconnect loop and closes the connection.
func (c *Controller) Stop() {
	c.mu.Lock()
	if !c.running {
		c.mu.Unlock()
		return
	}
	c.running = false
	stop, done := c.stop, c.done
	c.mu.Unlock()

	close(stop)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
	c.Disconnect()
}

func (c *Controller) reconnectLoop(stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)

	delay := time.Second
	for {
		if !c.Connected() {
			if err := c.Connect(); err != nil {
				c.reportDisconnected(err.Error())
				delay = min(time.Duration(c.maxReconnect())*time.Second, delay*2)
			} else {
				delay = time.Second
			}
		} else if err := c.probe(); err != nil {
			// A dead socket only shows up on use, so ask OBS something cheap.
			slog.Warn("lost the OBS connection", "error", err)
			c.Disconnect()
			c.reportDisconnected(err.Error())
			delay = time.Second
		} else {
			delay = 2 * time.Second
		}

		select {
		case <-stop:
			return
		case <-time.After(delay):
		}
	}
}

// probe makes the cheapest real request there is. Checking only that the
// client pointer is non-nil would miss a socket that died quietly, which is
// exactly the case this loop exists to catch.
func (c *Controller) probe() error {
	if c.Reloading() {
		// Asking anything now is the undefined behaviour the pause exists to
		// avoid, and a health check is not worth crashing OBS over.
		return nil
	}
	client, err := c.request()
	if err != nil {
		return err
	}
	if _, err := client.General.GetVersion(); err != nil {
		return err
	}
	return nil
}

func (c *Controller) maxReconnect() float64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.options.ReconnectMaxS
}

// reportDisconnected fires the callback only on the transition, so a closed
// OBS does not announce itself every two seconds for an hour.
func (c *Controller) reportDisconnected(reason string) {
	c.mu.Lock()
	announce := c.wasConnected
	c.wasConnected = false
	c.lastError = reason
	callback := c.options.OnDisconnected
	c.mu.Unlock()

	if announce && callback != nil {
		callback(reason)
	}
}

// watchEvents mirrors changes she made in OBS directly.
func (c *Controller) watchEvents(client *goobs.Client) {
	defer func() {
		if problem := recover(); problem != nil {
			slog.Debug("the OBS event stream ended", "reason", problem)
		}
	}()

	for raw := range client.IncomingEvents {
		c.mu.RLock()
		callback := c.options.OnEvent
		current := c.client
		c.mu.RUnlock()

		if current != client {
			return // this connection has been replaced
		}
		if callback == nil {
			continue
		}

		switch event := raw.(type) {
		case *events.CurrentProgramSceneChanged:
			callback(Event{Kind: "scene_changed", SceneName: event.SceneName})
		case *events.StreamStateChanged:
			callback(Event{Kind: "stream_state", Active: event.OutputActive})
		case *events.InputMuteStateChanged:
			callback(Event{
				Kind: "mute_changed", InputName: event.InputName, Muted: event.InputMuted,
			})
		case *events.CurrentProfileChanged:
			// The profile carries the stream key, so this is OBS saying she is
			// pointed at a different channel now -- whether MikkiLens asked for
			// the change or she picked it from the menu herself.
			callback(Event{Kind: "profile_changed", ProfileName: event.ProfileName})
		case *events.CurrentSceneCollectionChanging:
			c.setReloading(true)
			callback(Event{
				Kind: "collection_reloading", CollectionName: event.SceneCollectionName,
			})
		case *events.CurrentSceneCollectionChanged:
			c.setReloading(false)
			callback(Event{
				Kind: "collection_changed", CollectionName: event.SceneCollectionName,
			})
		}
	}
}

// request returns the live client, or an error explaining that OBS is not
// there. Every call below goes through it.
func (c *Controller) request() (*goobs.Client, error) {
	c.mu.RLock()
	client := c.client
	since := c.reloadingSince
	c.mu.RUnlock()

	if client == nil {
		return nil, &NotConnectedError{Reason: "not connected to OBS"}
	}
	if !since.IsZero() && time.Since(since) < reloadPatience {
		return nil, &ReloadingError{Reason: "OBS is loading a scene collection"}
	}
	return client, nil
}

// Reloading reports whether OBS is in the middle of a scene collection change.
func (c *Controller) Reloading() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return !c.reloadingSince.IsZero() && time.Since(c.reloadingSince) < reloadPatience
}

func (c *Controller) setReloading(active bool) {
	c.mu.Lock()
	if active {
		c.reloadingSince = time.Now()
	} else {
		c.reloadingSince = time.Time{}
	}
	c.mu.Unlock()
}

func (c *Controller) fail(err error) error {
	c.mu.Lock()
	c.lastError = err.Error()
	c.mu.Unlock()

	// goobs pairs a response with its request by reading the next one off a
	// shared channel, so a request that times out leaves its late response
	// waiting for the *following* request to collect -- which then fails on a
	// mismatched id, and so does every request after it, for good. The
	// connection is unusable from here, so it is dropped and the reconnect loop
	// builds a clean one rather than answering wrongly forever.
	message := err.Error()
	if strings.Contains(message, "timeout waiting for response") ||
		strings.Contains(message, "mismatched ID") {
		slog.Warn("dropping a desynchronised OBS connection", "error", err)
		c.Disconnect()
		c.reportDisconnected(message)
	}
	return &Error{Reason: message}
}

// -- scenes -------------------------------------------------------------------

// Scenes lists the scene names, top to bottom as OBS shows them.
func (c *Controller) Scenes() ([]string, error) {
	client, err := c.request()
	if err != nil {
		return nil, err
	}
	response, err := client.Scenes.GetSceneList()
	if err != nil {
		return nil, c.fail(err)
	}

	names := make([]string, 0, len(response.Scenes))
	for _, scene := range response.Scenes {
		names = append(names, scene.SceneName)
	}
	// OBS returns them bottom-up; reading them out in the order she sees them
	// is less confusing.
	for left, right := 0, len(names)-1; left < right; left, right = left+1, right-1 {
		names[left], names[right] = names[right], names[left]
	}
	return names, nil
}

// CurrentScene is the scene on the broadcast now.
func (c *Controller) CurrentScene() (string, error) {
	client, err := c.request()
	if err != nil {
		return "", err
	}
	response, err := client.Scenes.GetCurrentProgramScene()
	if err != nil {
		return "", c.fail(err)
	}
	if response.SceneName != "" {
		return response.SceneName, nil
	}
	return response.CurrentProgramSceneName, nil
}

// Inputs lists every input OBS has, with its kind.
func (c *Controller) Inputs() ([]Input, error) {
	client, err := c.request()
	if err != nil {
		return nil, err
	}
	response, err := client.Inputs.GetInputList()
	if err != nil {
		return nil, c.fail(err)
	}

	found := make([]Input, 0, len(response.Inputs))
	for _, input := range response.Inputs {
		found = append(found, Input{Name: input.InputName, Kind: input.InputKind})
	}
	return found, nil
}

// Input is one OBS input.
type Input struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
}

// -- microphone ---------------------------------------------------------------

// MicSourceName is the configured microphone, or the first audio input OBS has.
func (c *Controller) MicSourceName() (string, error) {
	found, err := c.Inputs()
	if err != nil {
		return "", err
	}

	names := make([]string, len(found))
	for index, input := range found {
		names[index] = input.Name
	}

	c.mu.RLock()
	configured := c.options.MicSource
	c.mu.RUnlock()

	if configured != "" {
		for _, name := range names {
			if name == configured {
				return name, nil
			}
		}
		if match, ok := bestName(configured, names); ok {
			return match, nil
		}
	}
	for _, input := range found {
		if inputCaptureKinds[input.Kind] {
			return input.Name, nil
		}
	}
	return "", &Error{Reason: "no microphone source found in OBS"}
}

// MicMuted reports whether her microphone is muted in OBS.
func (c *Controller) MicMuted() (bool, error) {
	name, err := c.MicSourceName()
	if err != nil {
		return false, err
	}
	client, err := c.request()
	if err != nil {
		return false, err
	}
	response, err := client.Inputs.GetInputMute(
		inputs.NewGetInputMuteParams().WithInputName(name))
	if err != nil {
		return false, c.fail(err)
	}
	return response.InputMuted, nil
}

// SetMicMuted mutes or unmutes her microphone.
func (c *Controller) SetMicMuted(muted bool) error {
	name, err := c.MicSourceName()
	if err != nil {
		return err
	}
	client, err := c.request()
	if err != nil {
		return err
	}
	if _, err := client.Inputs.SetInputMute(
		inputs.NewSetInputMuteParams().WithInputName(name).WithInputMuted(muted)); err != nil {
		return c.fail(err)
	}
	return nil
}

// -- streaming ----------------------------------------------------------------

// IsStreaming reports whether she is live.
func (c *Controller) IsStreaming() (bool, error) {
	client, err := c.request()
	if err != nil {
		return false, err
	}
	response, err := client.Stream.GetStreamStatus()
	if err != nil {
		return false, c.fail(err)
	}
	return response.OutputActive, nil
}

// StartStream goes live.
func (c *Controller) StartStream() error {
	client, err := c.request()
	if err != nil {
		return err
	}
	if _, err := client.Stream.StartStream(); err != nil {
		return c.fail(err)
	}
	return nil
}

// StopStream ends the broadcast.
func (c *Controller) StopStream() error {
	client, err := c.request()
	if err != nil {
		return err
	}
	if _, err := client.Stream.StopStream(); err != nil {
		return c.fail(err)
	}
	return nil
}

// -- accessibility helper -----------------------------------------------------

// CapturedOutputDevices lists the device ids OBS captures desktop audio from.
//
// It is used to warn when MikkiLens is about to speak into a device the stream
// is recording: the difference between private feedback and telling every
// viewer that her microphone just muted.
func (c *Controller) CapturedOutputDevices() ([]string, error) {
	found, err := c.Inputs()
	if err != nil {
		return nil, err
	}
	client, err := c.request()
	if err != nil {
		return nil, err
	}

	captured := []string{}
	for _, input := range found {
		if !outputCaptureKinds[input.Kind] {
			continue
		}
		response, err := client.Inputs.GetInputSettings(
			inputs.NewGetInputSettingsParams().WithInputName(input.Name))
		if err != nil {
			continue
		}
		if id, ok := response.InputSettings["device_id"].(string); ok && id != "" {
			captured = append(captured, id)
		}
	}
	return captured, nil
}

// MayCaptureDevice guesses whether OBS would put this output device on the
// broadcast.
func (c *Controller) MayCaptureDevice(deviceName string) bool {
	captured, err := c.CapturedOutputDevices()
	if err != nil || len(captured) == 0 {
		return false
	}
	for _, id := range captured {
		// "default" means whatever Windows is using, which is the common case
		// and the one worth warning about.
		if id == "default" {
			return true
		}
		if fuzzy.PartialRatio(strings.ToLower(deviceName), strings.ToLower(id)) > 80 {
			return true
		}
	}
	return false
}

// bestName fuzzy-matches a spoken name against the real OBS names.
func bestName(spoken string, candidates []string) (string, bool) {
	if len(candidates) == 0 {
		return "", false
	}
	lowered := strings.ToLower(strings.TrimSpace(spoken))
	if lowered == "" {
		return "", false
	}

	folded := make([]string, len(candidates))
	for index, name := range candidates {
		folded[index] = strings.ToLower(name)
		if folded[index] == lowered {
			return candidates[index], true
		}
	}
	index, score := fuzzy.ExtractOne(lowered, folded, fuzzy.WRatio)
	if index < 0 || score < nameMatchThreshold {
		return "", false
	}
	return candidates[index], true
}
