// Package trakteer watches a Trakteer notification overlay so chat stops being
// read while an alert is on screen.
//
// The same problem Tako has, and mostly the same shape of answer: the overlay
// is a browser source in OBS showing the alert and sometimes reading it out,
// and nothing tells MikkiLens that is happening.
//
// Where it differs is that the donation arrives whole. Tako sends a nudge and
// expects the overlay to come and ask what it was; Trakteer puts the supporter,
// the message and the media in the event itself, so there is nothing to fetch
// when one lands and no queue to race. The only thing read over HTTP is the
// creator's overlay configuration, which is how long an alert stays up, and
// that is read on connecting rather than per donation.
//
// Nothing in this package writes. Trakteer has no acknowledgement to send, so
// unlike Tako there is nothing here that could take a donation off her stream
// -- but the same rule applies for the same reason.
package trakteer

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/exzork/mikkilens/packages/controllers/pusher"
	"github.com/exzork/mikkilens/packages/core/config"
)

// notificationEvent is what Laravel broadcasts a notification as. The kind of
// donation it is lives in the payload rather than the event name, which is why
// there is only ever this one to listen for.
//
// No leading dot. Trakteer's overlay asks Laravel Echo for ".Illuminate\..."
// and the dot is Echo's own convention for "this is a raw name, do not put the
// application namespace in front of it" -- it is stripped before the
// subscription and never appears on the wire. Listening for the name with the
// dot still on it matches nothing at all, which is silent in exactly the way a
// working integration is.
const notificationEvent = `Illuminate\Notifications\Events\BroadcastNotificationCreated`

// isNotification reports whether an event name is the one above, with or
// without Echo's leading dot, so that either spelling is understood.
func isNotification(event string) bool {
	return strings.TrimPrefix(event, ".") == notificationEvent
}

// How long a tip id is remembered, so the same donation cannot be held for
// twice if Trakteer sends it on both channels.
const rememberFor = 15 * time.Minute

// The overlay configuration is re-read on this timer, which is how a duration
// changed in the dashboard is noticed without a restart.
const refreshEvery = 5 * time.Minute

// Donation is what one alert says, for when MikkiLens reads it out herself
// rather than leaving it to the overlay.
type Donation struct {
	Donor    string
	Quantity float64
	Unit     string
	Price    string // already formatted by Trakteer, e.g. "Rp 5.000"
	Message  string

	// Test marks the dashboard's example alert.
	Test bool
}

// Options configure one watcher.
type Options struct {
	Settings config.Trakteer
	Link     Link

	// OnHold says chat should stay quiet until the given moment. It may be
	// called again before the previous hold expires: a run of donations plays
	// one after another, and each call already accounts for the ones before it.
	OnHold func(until time.Time, donation Donation)

	OnStatus func(status, detail string)
}

// Watcher is one running connection to a Trakteer overlay.
type Watcher struct {
	mu       sync.Mutex
	settings config.Trakteer
	link     Link
	onHold   func(time.Time, Donation)
	onStatus func(string, string)
	origin   string
	client   *http.Client

	// held is when the quiet currently ends, and seenSettings is the overlay
	// configuration behind it.
	held         time.Time
	seenSettings settings
	seen         map[string]time.Time

	running bool
	cancel  context.CancelFunc
	done    chan struct{}
}

// New builds a watcher. Nothing connects until Start is called.
func New(options Options) *Watcher {
	return &Watcher{
		settings: options.Settings,
		link:     options.Link,
		onHold:   options.OnHold,
		onStatus: options.OnStatus,
		origin:   apiOrigin,
		client:   &http.Client{Timeout: 10 * time.Second},
		seen:     map[string]time.Time{},
	}
}

// Start connects and keeps connected until Stop.
func (w *Watcher) Start() {
	w.mu.Lock()
	if w.running || w.link.Key == "" || w.link.Hash == "" {
		w.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	w.running, w.cancel, w.done = true, cancel, make(chan struct{})
	done := w.done
	w.mu.Unlock()

	go w.run(ctx, done)
	go w.refresh(ctx)
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

// SetConfig applies changed tuning live. Changing the link needs a restart,
// which the engine does rather than reaching in here.
func (w *Watcher) SetConfig(settings config.Trakteer) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.settings = settings
}

func (w *Watcher) config() config.Trakteer {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.settings
}

// -- the connection -----------------------------------------------------------

func (w *Watcher) run(ctx context.Context, done chan struct{}) {
	defer close(done)

	channels := w.link.Channels()
	backoff := time.Second

	for ctx.Err() == nil {
		started := time.Now()
		w.status("connecting", channels[0])

		err := pusher.Listen(ctx, pusher.Options{
			Endpoint: relayEndpoint,
			Origin:   overlayOrigin,
			Channels: channels,
			OnReady: func() {
				// At the ordinary level, because this feature does nothing
				// visible when it is working: "it is watching" is the only
				// evidence there is that it is on at all.
				slog.Info("watching Trakteer donations", "channel", channels[0])
				w.status("connected", channels[0])
			},
			OnEvent: func(_, event string, data json.RawMessage) {
				w.onEvent(event, data)
			},
		})
		if ctx.Err() != nil {
			return
		}
		w.status("disconnected", err.Error())

		// A connection that stayed up was working; whatever knocked it over is
		// not the same problem as a link the relay will never accept, and it
		// should not inherit a long wait from one.
		if time.Since(started) > time.Minute {
			backoff = time.Second
		}
		slog.Warn("lost the Trakteer relay; reconnecting", "in", backoff, "error", err)

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

func (w *Watcher) onEvent(event string, data json.RawMessage) {
	if !isNotification(event) {
		slog.Debug("ignoring a Trakteer event", "event", event)
		return
	}

	var given tip
	if err := pusher.Unwrap(data, &given); err != nil {
		slog.Warn("could not understand a Trakteer donation", "error", err)
		return
	}
	if !given.isTip() {
		// Trakteer broadcasts more than tips on these channels -- goals and
		// leaderboards among them -- and none of it goes on screen as an alert.
		slog.Debug("ignoring a Trakteer notification", "type", given.Type)
		return
	}
	if w.alreadySeen(given.id()) {
		return
	}

	until := w.extend(holdFor(given, w.knownSettings(), w.config()))
	slog.Info("a donation is on screen; holding chat",
		"site", "trakteer", "donor", given.SupporterName,
		"units", given.Quantity, "unit", given.Unit,
		"real", given.isReal(), "until", until)

	if w.onHold != nil {
		w.onHold(until, Donation{
			Donor: given.SupporterName, Quantity: given.units(), Unit: given.Unit,
			Price: given.Price, Message: given.SupporterMessage, Test: !given.isReal(),
		})
	}
}

// refresh re-reads the overlay configuration, which is where a duration
// changed in the dashboard is noticed.
func (w *Watcher) refresh(ctx context.Context) {
	ticker := time.NewTicker(refreshEvery)
	defer ticker.Stop()

	// Once at the start, so a wrong link is reported on connecting rather than
	// five minutes into the stream.
	w.readSettings(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.readSettings(ctx)
		}
	}
}

func (w *Watcher) readSettings(ctx context.Context) {
	timed, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	found, err := fetchSettings(timed, w.client, w.origin, w.link.Key)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		slog.Warn("could not read the Trakteer overlay settings", "error", err)
		if errors.Is(err, errUnknownKey) {
			w.status("rejected", err.Error())
		} else {
			w.status("error", err.Error())
		}
		return
	}

	w.mu.Lock()
	w.seenSettings = found
	w.mu.Unlock()
	slog.Debug("read the Trakteer overlay settings", "delay", found.Delay, "tts", found.TTS)
}

// knownSettings is the overlay configuration from the last read. A donation
// arrives without it, so it has to be something already known.
func (w *Watcher) knownSettings() settings {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.seenSettings
}

// alreadySeen reports whether this donation has been held for, remembering it
// if it has not. Old ids are dropped on the way past, which is the only
// pruning this needs: it runs once per donation.
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
	slog.Debug("Trakteer", "status", status, "detail", detail)
	if w.onStatus != nil {
		w.onStatus(status, detail)
	}
}
