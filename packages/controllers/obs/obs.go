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
	"github.com/andreykaipov/goobs/api/requests/sceneitems"
	"github.com/andreykaipov/goobs/api/requests/scenes"

	"github.com/exzork/mikkilens/packages/core/fuzzy"
)

// nameMatchThreshold is how close a spoken name has to be before it counts as
// referring to a scene or source that exists.
const nameMatchThreshold = 65.0

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

// SceneItem is one source inside a scene.
type SceneItem struct {
	ID      int
	Name    string
	Enabled bool
}

// Event is a change she made in OBS directly, which the app mirrors so its
// state never drifts from what is actually on screen.
type Event struct {
	Kind      string // "scene_changed" | "stream_state" | "mute_changed"
	SceneName string
	InputName string
	Muted     bool
	Active    bool
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
		goobs.WithResponseTimeout(5*time.Second))
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
		}
	}
}

// request returns the live client, or an error explaining that OBS is not
// there. Every call below goes through it.
func (c *Controller) request() (*goobs.Client, error) {
	c.mu.RLock()
	client := c.client
	c.mu.RUnlock()

	if client == nil {
		return nil, &NotConnectedError{Reason: "not connected to OBS"}
	}
	return client, nil
}

func (c *Controller) fail(err error) error {
	c.mu.Lock()
	c.lastError = err.Error()
	c.mu.Unlock()
	return &Error{Reason: err.Error()}
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

// ResolveScene finds the real scene name a spoken one refers to.
func (c *Controller) ResolveScene(spoken string) (string, error) {
	names, err := c.Scenes()
	if err != nil {
		return "", err
	}
	found, ok := bestName(spoken, names)
	if !ok {
		return "", nil
	}
	return found, nil
}

// SwitchScene switches to the scene whose name best matches, and returns the
// real name so it can be read back to her.
func (c *Controller) SwitchScene(spoken string) (string, error) {
	actual, err := c.ResolveScene(spoken)
	if err != nil {
		return "", err
	}
	if actual == "" {
		return "", &Error{Reason: fmt.Sprintf("no scene matching %q", spoken)}
	}

	client, err := c.request()
	if err != nil {
		return "", err
	}
	if _, err := client.Scenes.SetCurrentProgramScene(
		scenes.NewSetCurrentProgramSceneParams().WithSceneName(actual)); err != nil {
		return "", c.fail(err)
	}
	return actual, nil
}

// -- sources ------------------------------------------------------------------

// SceneItems lists the sources in one scene, or in the current one.
func (c *Controller) SceneItems(scene string) ([]SceneItem, error) {
	client, err := c.request()
	if err != nil {
		return nil, err
	}
	if scene == "" {
		if scene, err = c.CurrentScene(); err != nil {
			return nil, err
		}
	}

	response, err := client.SceneItems.GetSceneItemList(
		sceneitems.NewGetSceneItemListParams().WithSceneName(scene))
	if err != nil {
		return nil, c.fail(err)
	}

	items := make([]SceneItem, 0, len(response.SceneItems))
	for _, item := range response.SceneItems {
		items = append(items, SceneItem{
			ID: item.SceneItemID, Name: item.SourceName, Enabled: item.SceneItemEnabled,
		})
	}
	return items, nil
}

// SetSourceVisible shows or hides a source, returning its real name.
func (c *Controller) SetSourceVisible(spoken string, visible bool) (string, error) {
	scene, err := c.CurrentScene()
	if err != nil {
		return "", err
	}
	items, err := c.SceneItems(scene)
	if err != nil {
		return "", err
	}

	names := make([]string, len(items))
	for index, item := range items {
		names[index] = item.Name
	}
	actual, ok := bestName(spoken, names)
	if !ok {
		return "", &Error{Reason: fmt.Sprintf("no source matching %q", spoken)}
	}

	client, err := c.request()
	if err != nil {
		return "", err
	}
	for _, item := range items {
		if item.Name != actual {
			continue
		}
		_, err := client.SceneItems.SetSceneItemEnabled(
			sceneitems.NewSetSceneItemEnabledParams().
				WithSceneName(scene).
				WithSceneItemId(item.ID).
				WithSceneItemEnabled(visible))
		if err != nil {
			return "", c.fail(err)
		}
		return actual, nil
	}
	return "", &Error{Reason: fmt.Sprintf("no source matching %q", spoken)}
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
