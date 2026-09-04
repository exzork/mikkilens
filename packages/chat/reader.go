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
	SayChat(text string, paid bool, onSpoken func(bool))
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
	r.bus.Clear(intent.PriorityDonation)
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
	r.bus.Clear(intent.PriorityDonation)
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

	// Past the message that says "and the others", the rest of a bulk gift is
	// dropped: the count has been given once already.
	if message.IsGiftReceived && settings.MaxGiftRecipients > 0 &&
		message.GiftIndex > settings.MaxGiftRecipients+1 &&
		message.GiftTotal > settings.MaxGiftRecipients {
		return false
	}
	for _, muted := range settings.MutedUsers {
		if strings.EqualFold(muted, message.Author) {
			return false
		}
	}
	// Exempting every event, not just super chats: a membership carries no
	// words of its own, so the emote-only filter silenced all of them.
	if settings.SkipEmoteOnly && message.IsEmoteOnly() && !message.IsEvent() {
		return false
	}
	if settings.CollapseDuplicates && message.Text != "" &&
		strings.TrimSpace(message.Text) == lastSpoken && !message.IsEvent() {
		return false
	}
	return true
}

// giftOverflow decides what to do with one recipient of a bulk gift.
//
// It reports how many are being folded away, and whether this message is the
// one that says so. Everything past that is dropped by shouldRead: the count
// has already been given, and repeating it fifty times would be worse than
// reading the names.
//
// A batch whose giving message was never seen has no index and no total, so
// nothing is folded and every recipient is read -- the same as before the cap
// existed, which is the safe way to be wrong.
func giftOverflow(message Message, settings config.Chat) (remaining int, folded bool) {
	cap := settings.MaxGiftRecipients
	if cap <= 0 || message.GiftIndex <= cap {
		return 0, false
	}
	if message.GiftTotal <= cap {
		return 0, false
	}
	return message.GiftTotal - cap, message.GiftIndex == cap+1
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

	case message.IsGift:
		// How many is the whole point: one is a kindness and fifty is an
		// event. The level is named when YouTube says which one it was.
		count := max(1, message.GiftCount)
		if message.GiftLevel != "" {
			return locale.T("chat.gift_level", i18n.Args{
				"author": message.Author, "count": count, "level": message.GiftLevel,
			})
		}
		return locale.T("chat.gift", i18n.Args{"author": message.Author, "count": count})

	case message.IsGiftReceived:
		// One past the cap stands in for the whole rest of the batch, so a
		// fifty-gift drop is a few names and a number rather than four minutes
		// of names.
		if remaining, folded := giftOverflow(message, settings); folded {
			return locale.T("chat.gift_others", i18n.Args{"count": remaining})
		}
		// Naming the giver is the point of reading these at all. When the
		// giving message was never seen -- connecting halfway through a batch
		// -- it says the rest rather than thanking nobody.
		if message.GifterName == "" {
			return locale.T("chat.gift_received_unknown", i18n.Args{"author": message.Author})
		}
		return locale.T("chat.gift_received", i18n.Args{
			"author": message.Author, "gifter": message.GifterName,
		})

	case message.IsMember:
		// A milestone says how long they have kept it, which is the thing
		// worth acknowledging; a new membership says which tier they chose.
		// Both fall back to the plain sentence when YouTube omits the detail,
		// which it does on some channels.
		switch {
		case message.MemberMonths > 0 && message.MemberLevel != "":
			return locale.T("chat.member_months_level", i18n.Args{
				"author": message.Author, "months": message.MemberMonths,
				"level": message.MemberLevel, "text": text,
			})
		case message.MemberMonths > 0:
			return locale.T("chat.member_months", i18n.Args{
				"author": message.Author, "months": message.MemberMonths, "text": text,
			})
		case message.MemberLevel != "" && message.IsMemberUpped:
			return locale.T("chat.member_upgraded", i18n.Args{
				"author": message.Author, "level": message.MemberLevel,
			})
		case message.MemberLevel != "":
			return locale.T("chat.member_level", i18n.Args{
				"author": message.Author, "level": message.MemberLevel,
			})
		}
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
		r.bus.SayChat(r.Render(message), message.IsPaid(), func(completed bool) {
			// Recorded only once it has actually been heard. An interrupted
			// message is re-read rather than lost, so marking it read when it
			// was merely queued would drop it if MikkiLens closed in between.
			if completed {
				r.ingest.ReadCursorFor().Record(message)
			}
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
