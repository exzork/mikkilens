package chat

import (
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/exzork/mikkilens/packages/core/config"
	"github.com/exzork/mikkilens/packages/core/i18n"
	"github.com/exzork/mikkilens/packages/core/intent"
)

// Playback is a cursor over the ingest buffer, never a tap on the connection.
//
// "Pause" stops speaking while messages keep arriving. "Resume" continues from
// where it stopped, oldest first, so nothing is skipped. "Skip to now" jumps
// the cursor forward and says how many were dropped, so the gap is never
// silent.

// minGap keeps messages from running into each other. Back-to-back speech with
// no seam is hard to follow.
const minGap = 50 * time.Millisecond

// Bus is the part of the speech bus the reader needs.
type Bus interface {
	SayChat(text string, superchat bool, onSpoken func(bool))
	Say(text string, priority intent.Priority)
	SayKey(key string, priority intent.Priority, args ...i18n.Args)
	Clear(priority intent.Priority) int
	InterruptCurrent()
}

// Reader reads chat aloud, with a cursor she controls.
type Reader struct {
	mu       sync.Mutex
	ingest   *Ingest
	bus      Bus
	locale   *i18n.Locale
	settings config.Chat

	cursor     int
	playing    bool
	lastSpoken string
	onBacklog  func(count int)

	running bool
	wake    chan struct{}
	stop    chan struct{}
	done    chan struct{}
}

// NewReader builds a reader over one ingest buffer.
func NewReader(ingest *Ingest, bus Bus, locale *i18n.Locale, settings config.Chat, onBacklog func(int)) *Reader {
	return &Reader{
		ingest: ingest, bus: bus, locale: locale,
		settings: settings, onBacklog: onBacklog,
	}
}

// Start begins reading.
//
// The cursor starts at the end: on connecting she wants what happens next, not
// an hour of history read at her.
func (r *Reader) Start(playing bool) {
	r.mu.Lock()
	if r.running {
		r.mu.Unlock()
		return
	}
	r.cursor = r.ingest.Len()
	r.playing = playing
	r.running = true
	r.wake = make(chan struct{}, 1)
	r.stop = make(chan struct{})
	r.done = make(chan struct{})
	stop, done := r.stop, r.done
	r.mu.Unlock()

	go r.run(stop, done)
}

// Stop ends reading.
func (r *Reader) Stop() {
	r.mu.Lock()
	if !r.running {
		r.mu.Unlock()
		return
	}
	r.running = false
	stop, done := r.stop, r.done
	r.mu.Unlock()

	close(stop)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
	}
}

// SetConfig applies changed chat settings live.
func (r *Reader) SetConfig(settings config.Chat) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.settings = settings
}

// SetLocale switches the language mid-run.
func (r *Reader) SetLocale(locale *i18n.Locale) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.locale = locale
}

// Notify tells the reader that new messages have landed.
func (r *Reader) Notify() {
	r.mu.Lock()
	wake := r.wake
	r.mu.Unlock()

	if wake != nil {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
	r.reportBacklog()
}

// Playing reports whether chat is being read.
func (r *Reader) Playing() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.playing
}

// Backlog is how many messages are waiting.
func (r *Reader) Backlog() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return max(0, r.ingest.Len()-r.cursor)
}

func (r *Reader) reportBacklog() {
	r.mu.Lock()
	callback := r.onBacklog
	r.mu.Unlock()
	if callback == nil {
		return
	}
	defer func() {
		if problem := recover(); problem != nil {
			slog.Error("backlog callback panicked", "panic", problem)
		}
	}()
	callback(r.Backlog())
}

// -- commands -----------------------------------------------------------------

// Pause stops reading. Messages keep arriving and keep their place.
func (r *Reader) Pause() bool {
	r.mu.Lock()
	if !r.playing {
		r.mu.Unlock()
		// A redundant pause still says something: silence here would read as
		// the command having been missed entirely.
		r.bus.SayKey("chat.already_paused", intent.PriorityResult)
		return false
	}
	r.playing = false
	r.mu.Unlock()

	// Cut off the sentence in progress rather than finishing it.
	r.bus.InterruptCurrent()
	r.bus.Clear(intent.PriorityChat)
	r.bus.SayKey("chat.paused", intent.PriorityResult)
	return true
}

// Resume starts reading again, from where it stopped.
func (r *Reader) Resume() bool {
	r.mu.Lock()
	if r.playing {
		r.mu.Unlock()
		r.bus.SayKey("chat.already_playing", intent.PriorityResult)
		return false
	}
	r.playing = true
	r.mu.Unlock()

	r.bus.SayKey("chat.resumed", intent.PriorityResult)
	r.Notify()
	return true
}

// SkipToNow jumps to the newest message, saying how many were skipped so the
// gap is never silent.
func (r *Reader) SkipToNow() int {
	r.mu.Lock()
	total := r.ingest.Len()
	skipped := max(0, total-r.cursor)
	r.cursor = total
	locale := r.locale
	r.mu.Unlock()

	r.bus.Clear(intent.PriorityChat)
	r.bus.InterruptCurrent()

	if skipped > 0 {
		r.bus.Say(locale.T("chat.skipped", i18n.Args{"count": skipped}), intent.PriorityResult)
	} else {
		r.bus.SayKey("chat.up_to_date", intent.PriorityResult)
	}
	r.reportBacklog()
	return skipped
}

// ReportBacklog says how far behind she is.
func (r *Reader) ReportBacklog() int {
	backlog := r.Backlog()
	if backlog == 0 {
		r.bus.SayKey("chat.up_to_date", intent.PriorityResult)
		return 0
	}

	r.mu.Lock()
	locale := r.locale
	r.mu.Unlock()

	r.bus.Say(locale.T("chat.behind", i18n.Args{
		"count": backlog, "minutes": r.backlogMinutes(),
	}), intent.PriorityResult)
	return backlog
}

func (r *Reader) backlogMinutes() int {
	pending := r.PendingMessages()
	if len(pending) == 0 {
		return 0
	}
	age := float64(time.Now().UnixNano())/1e9 - pending[0].ReceivedAt
	return max(1, int(age/60+0.5))
}

// PendingMessages is everything not yet read, which is what a summary is made
// from.
func (r *Reader) PendingMessages() []Message {
	r.mu.Lock()
	cursor := r.cursor
	r.mu.Unlock()
	pending, _ := r.ingest.From(cursor)
	return pending
}

// -- filtering ----------------------------------------------------------------

func (r *Reader) shouldRead(message Message) bool {
	r.mu.Lock()
	settings := r.settings
	lastSpoken := r.lastSpoken
	r.mu.Unlock()

	for _, muted := range settings.MutedUsers {
		if strings.EqualFold(muted, message.Author) {
			return false
		}
	}
	if settings.SkipEmoteOnly && message.IsEmoteOnly() && !message.IsSuperchat {
		return false
	}
	if settings.CollapseDuplicates && message.Text != "" &&
		strings.TrimSpace(message.Text) == lastSpoken && !message.IsSuperchat {
		return false
	}
	return true
}

// Render turns a message into the sentence that gets spoken.
func (r *Reader) Render(message Message) string {
	r.mu.Lock()
	settings, locale := r.settings, r.locale
	r.mu.Unlock()

	text := trimTo(message.Text, settings.MaxMessageChars)
	switch {
	case message.IsSuperchat:
		return locale.T("chat.superchat", i18n.Args{
			"author": message.Author, "amount": message.Amount, "text": text,
		})
	case message.IsMember:
		return locale.T("chat.member", i18n.Args{"author": message.Author})
	default:
		return locale.T("chat.message", i18n.Args{"author": message.Author, "text": text})
	}
}

// -- worker -------------------------------------------------------------------

// next takes the message to read, preferring super chats when configured.
func (r *Reader) next() (Message, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	pending, total := r.ingest.From(r.cursor)
	if r.cursor >= total || len(pending) == 0 {
		return Message{}, false
	}

	if r.settings.ReadSuperchatsFirst {
		for offset, message := range pending {
			if !message.IsSuperchat {
				continue
			}
			// Pull it out of the queue without disturbing the rest, so the
			// ordinary messages behind it keep their place.
			r.ingest.Remove(r.cursor + offset)
			return message, true
		}
	}

	message := pending[0]
	r.cursor++
	return message, true
}

func (r *Reader) run(stop <-chan struct{}, done chan struct{}) {
	defer close(done)

	r.mu.Lock()
	wake := r.wake
	r.mu.Unlock()

	idle := time.NewTicker(250 * time.Millisecond)
	defer idle.Stop()

	for {
		select {
		case <-stop:
			return
		default:
		}

		if !r.Playing() {
			select {
			case <-stop:
				return
			case <-wake:
			case <-idle.C:
			}
			continue
		}

		message, ok := r.next()
		if !ok {
			select {
			case <-stop:
				return
			case <-wake:
			case <-idle.C:
			}
			continue
		}
		if !r.shouldRead(message) {
			continue
		}

		spoken := make(chan struct{})
		var once sync.Once
		r.bus.SayChat(r.Render(message), message.IsSuperchat, func(bool) {
			once.Do(func() { close(spoken) })
		})

		r.mu.Lock()
		r.lastSpoken = strings.TrimSpace(message.Text)
		r.mu.Unlock()
		r.reportBacklog()

		// Wait for it to finish, so messages cannot pile up faster than they
		// can be heard, but stay responsive to a pause.
		if r.waitSpoken(spoken, stop) {
			return
		}
		time.Sleep(minGap)
	}
}

// waitSpoken blocks until the message has been read, the reader is paused, or
// the reader stops. It reports whether the reader is shutting down.
func (r *Reader) waitSpoken(spoken <-chan struct{}, stop <-chan struct{}) bool {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-spoken:
			return false
		case <-stop:
			return true
		case <-ticker.C:
			if !r.Playing() {
				return false
			}
		}
	}
}
