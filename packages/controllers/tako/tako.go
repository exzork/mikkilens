// Package tako watches a Tako donation overlay so chat stops being read while
// an alert is on screen.
//
// Tako's own overlay is a browser source in OBS: it shows the alert and reads
// the donation out in its own voice. Nothing tells MikkiLens that is
// happening, so without this she talks over every donation -- the one message
// on the stream somebody paid to be heard.
//
// What arrives over the relay is a nudge, not a payload: an event saying
// something landed, after which the overlay endpoint is asked what it was.
// That is the same shape the browser source uses, and MikkiLens deliberately
// only does the reading half of it. The endpoint's other half, the PUT saying
// an alert has been played, is what moves Tako's queue on. It belongs to the
// browser source that is actually showing the alerts, and sending it from here
// would take donations off her stream -- so nothing in this package writes.
package tako

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/exzork/mikkilens/packages/controllers/pusher"
	"github.com/exzork/mikkilens/packages/core/config"
)

// How long a gift id is remembered, so the periodic catch-up below cannot hold
// chat a second time for an alert that has already been and gone.
const rememberFor = 15 * time.Minute

// The relay is the way donations are meant to arrive. The poll is a safety
// net: it picks up the overlay's duration settings when she changes them in
// the dashboard, and it catches an alert whose event went missing.
const catchUpEvery = 5 * time.Minute

// FormatAmount writes a donation amount the way it would be read out.
//
// Tako sends a bare number and a currency code. Grouping the thousands is the
// difference between "ten thousand" and a string of digits nobody can hear the
// size of, and rupiah groups with dots rather than commas.
func FormatAmount(amount float64, currency string) string {
	whole := int64(amount + 0.5)
	digits := strconv.FormatInt(whole, 10)

	var grouped strings.Builder
	for index, digit := range digits {
		if index > 0 && (len(digits)-index)%3 == 0 {
			grouped.WriteByte('.')
		}
		grouped.WriteRune(digit)
	}

	switch strings.ToLower(strings.TrimSpace(currency)) {
	case "", "idr":
		return "Rp" + grouped.String()
	default:
		return grouped.String() + " " + strings.ToUpper(currency)
	}
}

// ParseLink pulls the overlay key out of an alert overlay address.
//
// A bare key is accepted too. The dashboard hands out a whole link and that is
// what anyone has to hand, but the config used to take only the key out of the
// middle of it, and a setting that used to work should keep working.
func ParseLink(link string) (string, error) {
	trimmed := strings.TrimSpace(link)
	if trimmed == "" {
		return "", errors.New("the Tako overlay link is empty")
	}
	if !strings.Contains(trimmed, "://") {
		// Already just the key.
		return trimmed, nil
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("could not understand the Tako overlay link: %w", err)
	}
	key := strings.TrimSpace(parsed.Query().Get("overlay_key"))
	if key == "" {
		return "", errors.New(
			"the Tako overlay link has no overlay_key in it; " +
				"copy the whole alert overlay address from the dashboard")
	}
	return key, nil
}

// Donation is what one alert says, for when MikkiLens reads it out herself
// rather than leaving it to the overlay.
type Donation struct {
	Donor    string
	Amount   float64
	Currency string
	Message  string

	// Test marks the dashboard's example alert, so a test can be recognised in
	// the log rather than looking like a donation nobody remembers receiving.
	Test bool
}

// Options configure one watcher.
type Options struct {
	Settings config.Tako
	Key      string

	// OnHold says chat should stay quiet until the given moment. It is called
	// with the donor's name only so the log can say who, and it may be called
	// again before the previous hold expires: a run of donations plays one
	// after another, and each call already accounts for the ones before it.
	OnHold func(until time.Time, donation Donation)

	// OnStatus reports the connection the way the chat ingest does, so the
	// same places that show chat being up or down can show this too.
	OnStatus func(status, detail string)
}

// Watcher is one running connection to a Tako overlay.
type Watcher struct {
	mu       sync.Mutex
	settings config.Tako
	key      string
	onHold   func(time.Time, Donation)
	onStatus func(string, string)
	origin   string
	client   *http.Client

	// held is when the quiet currently ends. Donations that arrive together
	// are played one after another by the overlay, so each new alert extends
	// from the end of the last rather than overlapping it.
	held time.Time
	seen map[string]time.Time

	// seenSettings is the overlay configuration from the last read, kept
	// because a test alert arrives carrying none and still has to be held for
	// the right length of time.
	seenSettings settings

	running bool
	cancel  context.CancelFunc
	done    chan struct{}
	poke    chan struct{}
}

// New builds a watcher. Nothing connects until Start is called.
func New(options Options) *Watcher {
	return &Watcher{
		settings: options.Settings,
		key:      options.Key,
		onHold:   options.OnHold,
		onStatus: options.OnStatus,
		origin:   overlayOrigin,
		client:   &http.Client{Timeout: 10 * time.Second},
		seen:     map[string]time.Time{},
		poke:     make(chan struct{}, 1),
	}
}

// Start connects and keeps connected until Stop.
func (w *Watcher) Start() {
	w.mu.Lock()
	if w.running || w.key == "" {
		w.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	w.running, w.cancel, w.done = true, cancel, make(chan struct{})
	done := w.done
	w.mu.Unlock()

	go w.run(ctx, done)
	go w.catchUp(ctx)
}

// Stop disconnects.
func (w *Watcher) Stop() {
	w.mu.Lock()
	if !w.running {
		w.mu.Unlock()
		return
	}
	w.running = false
	cancel, done := w.cancel, w.done
	w.mu.Unlock()

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
	}
}

// Running reports whether the watcher is connected or trying to be.
func (w *Watcher) Running() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.running
}

// SetConfig applies changed tuning live. Changing the key needs a restart,
// which the engine does rather than reaching in here.
func (w *Watcher) SetConfig(settings config.Tako) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.settings = settings
}

func (w *Watcher) config() config.Tako {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.settings
}

// -- the connection -----------------------------------------------------------

func (w *Watcher) run(ctx context.Context, done chan struct{}) {
	defer close(done)

	channel := "overlay." + sanitize(w.key)
	backoff := time.Second

	for ctx.Err() == nil {
		started := time.Now()
		w.status("connecting", channel)

		err := pusher.Listen(ctx, pusher.Options{
			Endpoint: relayEndpoint,
			Origin:   overlayOrigin,
			Channels: []string{channel},
			OnReady: func() {
				// Worth a line at the ordinary level, unlike the rest of the
				// housekeeping: this feature does nothing visible when it is
				// working, so "it is watching" is the only evidence there is
				// that it is on at all.
				slog.Info("watching Tako donations", "channel", channel)
				w.status("connected", channel)
			},
			OnEvent: func(_, event string, data json.RawMessage) {
				w.onEvent(ctx, event, data)
			},
		})
		if ctx.Err() != nil {
			return
		}
		w.status("disconnected", err.Error())

		// A connection that stayed up was working; whatever knocked it over is
		// not the same problem as a key the relay will never accept, and it
		// should not inherit a long wait from one.
		if time.Since(started) > time.Minute {
			backoff = time.Second
		}
		slog.Warn("lost the Tako relay; reconnecting", "in", backoff, "error", err)

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff = backoff * 2; backoff > 30*time.Second {
			backoff = 30 * time.Second
		}
	}
}

func (w *Watcher) onEvent(ctx context.Context, event string, data json.RawMessage) {
	switch event {
	case "messages":
		// Says something landed without saying what, exactly as it does for
		// the browser source, which then asks the endpoint what it was.
		w.check(ctx)
	case "command":
		w.onCommand(data)
	default:
		// A banned gift, a played-gift acknowledgement. For the overlay to act
		// on, and neither changes whether MikkiLens should be quiet.
		slog.Debug("ignoring a Tako event", "event", event)
	}
}

// onCommand handles the buttons on the Tako dashboard.
//
// Only the test alert, and it is here for a specific reason: pressing Test is
// the first thing anyone does after setting this up, and it is the one alert
// that never reaches the donation queue. The overlay builds the example in the
// browser and plays it locally, so no gift is created and no "messages" event
// is sent. Without this, the check that is supposed to prove the feature works
// is the one case that proves nothing, and it fails in the direction that
// looks exactly like a fault.
func (w *Watcher) onCommand(data json.RawMessage) {
	// The payload is a string like "alert.test" or "alert.test.vn": which
	// overlay it is aimed at, what to do, and sometimes a variant.
	var payload string
	if err := json.Unmarshal(data, &payload); err != nil {
		slog.Debug("could not understand a Tako command", "data", string(data))
		return
	}

	parts := strings.Split(payload, ".")
	if len(parts) < 2 {
		return
	}
	target, command := parts[0], parts[1]
	if target != "alert" && target != "*" {
		return
	}
	if command != "test" {
		slog.Debug("ignoring a Tako command", "command", payload)
		return
	}

	variant := ""
	if len(parts) > 2 {
		variant = parts[2]
	}

	until := w.extend(holdFor(w.exampleAlert(variant), w.config()))
	slog.Info("a test alert is on screen; holding chat", "variant", variant, "until", until)

	if w.onHold != nil {
		example := w.exampleAlert(variant)
		w.onHold(until, Donation{
			Donor: exampleDonor, Amount: exampleAmount, Currency: "idr",
			Message: example.Message, Test: true,
		})
	}
}

// exampleAlert is the alert the overlay makes up for a test, rebuilt here from
// the same pieces so that a test is held for as long as the real thing would
// be.
func (w *Watcher) exampleAlert(variant string) alert {
	found := alert{Settings: w.knownSettings()}
	found.Sender.Name = exampleDonor
	found.Amount = exampleAmount
	found.Message = exampleMessage
	if variant == "vn" {
		found.RecordingURL = exampleRecording
	}
	return found
}

// knownSettings is the overlay configuration from the last time it was read.
//
// A test arrives with nothing attached, so the duration it should be held for
// has to come from somewhere already known. The catch-up runs once on
// connecting, which is what makes sure there is something here.
func (w *Watcher) knownSettings() settings {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.seenSettings
}

// catchUp re-reads the overlay on a slow timer, which is where a duration
// changed in the dashboard is noticed and where an alert whose event went
// missing is picked up.
func (w *Watcher) catchUp(ctx context.Context) {
	ticker := time.NewTicker(catchUpEvery)
	defer ticker.Stop()

	// Once at the start, so a wrong key is reported on connecting rather than
	// five minutes into the stream.
	w.check(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.check(ctx)
		}
	}
}

// check asks what is on the overlay and holds chat for as long as it will be
// there.
func (w *Watcher) check(ctx context.Context) {
	// Serialised: two events arriving together must not both decide they are
	// the first to see the same alert.
	select {
	case w.poke <- struct{}{}:
		defer func() { <-w.poke }()
	case <-ctx.Done():
		return
	}

	timed, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	found, waiting, err := fetch(timed, w.client, w.origin, w.key)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("could not read the Tako overlay", "error", err)
			if errors.Is(err, errUnknownKey) {
				w.status("rejected", err.Error())
			} else {
				w.status("error", err.Error())
			}
		}
		return
	}
	// Kept from every successful read, alert or not: a 206 carries no alert but
	// does carry the configuration, and that is what a test alert is measured
	// against.
	w.mu.Lock()
	w.seenSettings = found.Settings
	w.mu.Unlock()

	if !waiting {
		return
	}

	id := found.Meta.ID
	if w.alreadySeen(id) {
		return
	}

	until := w.extend(holdFor(found, w.config()))
	donor := found.Sender.Name
	slog.Info("a donation is on screen; holding chat",
		"donor", donor, "amount", found.Amount, "until", until)

	if w.onHold != nil {
		w.onHold(until, Donation{
			Donor: donor, Amount: found.Amount, Currency: found.Meta.Currency,
			Message: found.Message, Test: found.Meta.IsTest,
		})
	}
}

// alreadySeen reports whether this alert has been held for, remembering it if
// it has not. Old ids are dropped on the way past, which is the only pruning
// this needs: it runs once per donation.
func (w *Watcher) alreadySeen(id string) bool {
	if id == "" {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()

	_, seen := w.seen[id]
	now := time.Now()
	for old, at := range w.seen {
		if now.Sub(at) > rememberFor {
			delete(w.seen, old)
		}
	}
	w.seen[id] = now
	return seen
}

// extend books another stretch of quiet on the end of whatever is already
// booked, because the overlay plays a run of donations one after another
// rather than all at once.
func (w *Watcher) extend(duration time.Duration) time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()

	from := time.Now()
	if w.held.After(from) {
		from = w.held
	}
	w.held = from.Add(duration)
	return w.held
}

func (w *Watcher) status(status, detail string) {
	slog.Debug("Tako", "status", status, "detail", detail)
	if w.onStatus != nil {
		w.onStatus(status, detail)
	}
}

// sanitize matches what Tako's own page does to a key before it names a
// channel with it, so a mistyped key lands on the same dead channel there as
// it does here rather than on a malformed subscription.
func sanitize(key string) string {
	kept := make([]rune, 0, len(key))
	for _, character := range key {
		switch {
		case character >= 'a' && character <= 'z',
			character >= 'A' && character <= 'Z',
			character >= '0' && character <= '9':
			kept = append(kept, character)
		case character == '_' || character == '-' || character == '=' ||
			character == '@' || character == ',' || character == '.' ||
			character == ';':
			kept = append(kept, character)
		}
	}
	if len(kept) == 0 {
		return "INVALID"
	}
	return string(kept)
}
