// Package feedback is the speech bus: one priority queue that owns the output
// device.
//
// Every subsystem speaks through here, which is what makes audible
// confirmation structural rather than something each feature has to remember.
// The priority ordering encodes one rule -- an error must never wait behind
// forty queued chat messages -- and interrupted chat is put back on the queue
// rather than dropped, so preemption never silently loses a message.
package feedback

import (
	"container/heap"
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/exzork/mikkilens/packages/audio/devices"
	"github.com/exzork/mikkilens/packages/audio/earcons"
	"github.com/exzork/mikkilens/packages/audio/tts"
	"github.com/exzork/mikkilens/packages/core/config"
	"github.com/exzork/mikkilens/packages/core/i18n"
	"github.com/exzork/mikkilens/packages/core/intent"
)

// Priority is re-exported from intent so callers only import one of them. Lower
// values win and preempt everything above them.
type Priority = intent.Priority

const (
	Error   = intent.PriorityError
	Confirm = intent.PriorityConfirm
	Result  = intent.PriorityResult
	Chat    = intent.PriorityChat
)

// Utterance is one thing waiting to be said.
type Utterance struct {
	Text     string
	Priority Priority
	Earcon   string
	Voice    string // empty means chosen from the priority and config
	Rate     string

	// RequeueIfInterrupted puts the utterance back at its original place in the
	// queue when something preempts it. Chat sets it, so being cut off by an
	// error means the message is re-read rather than lost.
	RequeueIfInterrupted bool

	// OnSpoken reports whether the utterance finished. The chat reader waits on
	// it so messages cannot pile up faster than they can be heard.
	OnSpoken func(completed bool)

	created time.Time
}

// Spoken is one line of the log page in the settings app.
type Spoken struct {
	Text      string  `json:"text"`
	Priority  string  `json:"priority"`
	Completed bool    `json:"completed"`
	At        float64 `json:"at"`
}

// Player is the part of the speaker the bus needs. Narrowing it to this is
// what lets the queue logic be tested without any audio hardware.
type Player interface {
	Play(tts.Audio) (bool, error)
	Stop()
	SetDevice(*devices.Device)
}

// Synthesizer turns text into audio. Swappable for the same reason.
type Synthesizer func(ctx context.Context, text string, options tts.Options) (tts.Audio, error)

const historyLimit = 200

// Bus serializes all speech onto one device, with preemption.
type Bus struct {
	mu         sync.Mutex
	cond       *sync.Cond
	queue      utteranceHeap
	counter    int
	current    *Utterance
	running    bool
	idle       chan struct{}
	history    []Spoken
	worker     sync.WaitGroup
	stopping   chan struct{}
	settings   config.Config
	locale     *i18n.Locale
	player     Player
	synthesize Synthesizer
}

// New builds a bus around one output device.
func New(settings config.Config, locale *i18n.Locale, device *devices.Device) *Bus {
	return NewWith(settings, locale, tts.NewSpeaker(device), tts.Synthesize)
}

// NewWith builds a bus with an explicit player and synthesizer, which is how
// the tests get instant, silent speech.
func NewWith(settings config.Config, locale *i18n.Locale, player Player, synthesize Synthesizer) *Bus {
	bus := &Bus{
		settings:   settings,
		locale:     locale,
		player:     player,
		synthesize: synthesize,
		idle:       make(chan struct{}),
		stopping:   make(chan struct{}),
	}
	bus.cond = sync.NewCond(&bus.mu)
	close(bus.idle) // an empty bus starts out idle
	return bus
}

// -- lifecycle ----------------------------------------------------------------

// Start begins speaking whatever is queued.
func (b *Bus) Start() {
	b.mu.Lock()
	if b.running {
		b.mu.Unlock()
		return
	}
	b.running = true
	b.stopping = make(chan struct{})
	b.mu.Unlock()

	b.worker.Add(1)
	go b.run()
}

// Stop drops the queue and shuts the worker down.
func (b *Bus) Stop() {
	b.mu.Lock()
	if !b.running {
		b.mu.Unlock()
		return
	}
	b.running = false
	b.queue = nil
	close(b.stopping)
	b.markIdleLocked()
	b.cond.Broadcast()
	b.mu.Unlock()

	b.player.Stop()
	b.worker.Wait()
}

// SetDevice changes where the voice comes out.
func (b *Bus) SetDevice(device *devices.Device) { b.player.SetDevice(device) }

// SetConfig applies changed settings without a restart.
func (b *Bus) SetConfig(settings config.Config) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.settings = settings
}

// Config is the settings currently in force.
func (b *Bus) Config() config.Config {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.settings
}

// SetLocale switches the language mid-run.
func (b *Bus) SetLocale(locale *i18n.Locale) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.locale = locale
}

// Locale is the language currently in use.
func (b *Bus) Locale() *i18n.Locale {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.locale
}

// -- queueing -----------------------------------------------------------------

// Say queues something to be said and returns immediately.
func (b *Bus) Say(text string, priority Priority) {
	b.Enqueue(Utterance{Text: text, Priority: priority})
}

// SayEarcon queues speech behind a tone.
func (b *Bus) SayEarcon(text string, priority Priority, earcon string) {
	b.Enqueue(Utterance{Text: text, Priority: priority, Earcon: earcon})
}

// SayKey queues a locale key. This is the normal way to say anything fixed.
func (b *Bus) SayKey(key string, priority Priority, args ...i18n.Args) {
	b.Enqueue(Utterance{
		Text:     b.Locale().T(key, args...),
		Priority: priority,
		Earcon:   defaultEarcon(priority),
	})
}

// Error queues a locale key at the highest priority, so it preempts whatever
// is being said.
func (b *Bus) Error(key string, args ...i18n.Args) {
	b.Enqueue(Utterance{
		Text:     b.Locale().T(key, args...),
		Priority: Error,
		Earcon:   "error",
	})
}

// SayChat queues a chat message. Interrupted messages are re-read, not lost.
func (b *Bus) SayChat(text string, superchat bool, onSpoken func(bool)) {
	settings, locale := b.Config(), b.Locale()
	earcon := "chat"
	if superchat {
		earcon = "superchat"
	}
	b.Enqueue(Utterance{
		Text:                 text,
		Priority:             Chat,
		Earcon:               earcon,
		Voice:                settings.VoiceForChat(locale.DefaultVoice()),
		Rate:                 settings.Speech.ChatRate,
		RequeueIfInterrupted: true,
		OnSpoken:             onSpoken,
	})
}

// Enqueue adds an utterance, preempting anything less important that is
// already being spoken.
func (b *Bus) Enqueue(utterance Utterance) {
	if trimmed := trimSpace(utterance.Text); trimmed == "" {
		slog.Warn("refusing to queue empty speech", "priority", utterance.Priority.String())
		return
	} else {
		utterance.Text = trimmed
	}
	utterance.created = time.Now()

	if utterance.Earcon != "" {
		b.Earcon(utterance.Earcon)
	}

	b.mu.Lock()
	b.counter++
	heap.Push(&b.queue, queued{
		priority: int(utterance.Priority),
		sequence: b.counter,
		what:     utterance,
	})
	b.markBusyLocked()
	preempt := b.current != nil && utterance.Priority < b.current.Priority
	b.cond.Signal()
	b.mu.Unlock()

	if preempt {
		b.player.Stop()
	}
}

// Earcon plays a tone immediately, bypassing the queue entirely.
//
// This is the whole point of earcons: the acknowledgement lands the instant the
// hotkey is pressed, about a second before any synthesized voice could, and it
// plays over whatever is already being said.
func (b *Bus) Earcon(name string) {
	settings := b.Config()
	wave, err := earcons.Render(name, settings.Speech.EarconVolume)
	if err != nil {
		slog.Error("unknown earcon", "name", name, "error", err)
		return
	}
	device := b.currentDevice()
	go func() {
		if _, err := devices.Play(device, wave, earcons.SampleRate, 1, nil); err != nil {
			slog.Warn("could not play an earcon", "name", name, "error", err)
		}
	}()
}

func (b *Bus) currentDevice() *devices.Device {
	if speaker, ok := b.player.(*tts.Speaker); ok {
		return speaker.Device()
	}
	return nil
}

// -- queue control ------------------------------------------------------------

// Clear drops everything pending at one priority and reports how many went.
//
// "Skip to now" uses it to throw away the chat backlog without touching a
// confirmation prompt that is still waiting for an answer.
func (b *Bus) Clear(priority Priority) int {
	b.mu.Lock()
	defer b.mu.Unlock()

	before := len(b.queue)
	kept := b.queue[:0]
	for _, entry := range b.queue {
		if entry.priority != int(priority) {
			kept = append(kept, entry)
		}
	}
	b.queue = kept
	heap.Init(&b.queue)

	if len(b.queue) == 0 && b.current == nil {
		b.markIdleLocked()
	}
	b.cond.Broadcast()
	return before - len(b.queue)
}

// ClearAll drops everything pending.
func (b *Bus) ClearAll() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	dropped := len(b.queue)
	b.queue = nil
	if b.current == nil {
		b.markIdleLocked()
	}
	b.cond.Broadcast()
	return dropped
}

// InterruptCurrent cuts off whatever is being said right now.
func (b *Bus) InterruptCurrent() { b.player.Stop() }

// Pending is how many utterances are waiting.
func (b *Bus) Pending() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.queue)
}

// WaitUntilIdle blocks until nothing is queued or being spoken.
func (b *Bus) WaitUntilIdle(timeout time.Duration) bool {
	b.mu.Lock()
	idle := b.idle
	b.mu.Unlock()

	if timeout <= 0 {
		<-idle
		return true
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-idle:
		return true
	case <-timer.C:
		return false
	}
}

// History is what has been said, newest last, capped so a long stream cannot
// grow it without bound.
func (b *Bus) History() []Spoken {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]Spoken(nil), b.history...)
}

// -- worker -------------------------------------------------------------------

func (b *Bus) run() {
	defer b.worker.Done()

	for {
		b.mu.Lock()
		for b.running && len(b.queue) == 0 {
			b.markIdleLocked()
			b.cond.Wait()
		}
		if !b.running {
			b.mu.Unlock()
			return
		}
		entry := heap.Pop(&b.queue).(queued)
		utterance := entry.what
		b.current = &utterance
		b.mu.Unlock()

		completed := b.speak(utterance)

		b.mu.Lock()
		b.current = nil
		if !completed && utterance.RequeueIfInterrupted && b.running {
			// Back at its original position, so an interrupted message is read
			// again rather than dropped or shuffled behind newer ones.
			heap.Push(&b.queue, entry)
		} else if len(b.queue) == 0 {
			b.markIdleLocked()
		}
		b.record(utterance, completed)
		b.mu.Unlock()

		if utterance.OnSpoken != nil {
			b.notify(utterance, completed)
		}
	}
}

func (b *Bus) speak(utterance Utterance) bool {
	settings, locale := b.Config(), b.Locale()

	voice := utterance.Voice
	if voice == "" {
		voice = settings.Voice(locale.DefaultVoice())
	}
	rate := utterance.Rate
	if rate == "" {
		rate = settings.Speech.Rate
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	audio, err := b.synthesize(ctx, utterance.Text, tts.Options{
		Voice: voice, Rate: rate, Volume: settings.Speech.Volume,
		NoCache: utterance.Priority == Chat,
	})
	if err != nil {
		slog.Error("could not synthesize speech", "text", clip(utterance.Text, 60), "error", err)
		b.Earcon("error")
		// Not requeued: retrying would fail in exactly the same way, and a
		// message that never stops being retried blocks everything behind it.
		return true
	}

	completed, err := b.player.Play(audio)
	if err != nil {
		slog.Error("could not play speech", "error", err)
		b.Earcon("error")
		return true
	}
	return completed
}

func (b *Bus) record(utterance Utterance, completed bool) {
	b.history = append(b.history, Spoken{
		Text:      utterance.Text,
		Priority:  utterance.Priority.String(),
		Completed: completed,
		At:        float64(time.Now().UnixNano()) / 1e9,
	})
	if len(b.history) > historyLimit {
		b.history = b.history[len(b.history)-historyLimit:]
	}
}

// notify keeps a bad callback from taking the bus down with it.
func (b *Bus) notify(utterance Utterance, completed bool) {
	defer func() {
		if problem := recover(); problem != nil {
			slog.Error("speech callback panicked", "panic", problem)
		}
	}()
	utterance.OnSpoken(completed)
}

func (b *Bus) markIdleLocked() {
	select {
	case <-b.idle: // already closed
	default:
		close(b.idle)
	}
}

func (b *Bus) markBusyLocked() {
	select {
	case <-b.idle:
		b.idle = make(chan struct{})
	default:
	}
}

func defaultEarcon(priority Priority) string {
	switch priority {
	case Error:
		return "error"
	case Confirm:
		return "confirm"
	case Chat:
		return "chat"
	default:
		return ""
	}
}

func clip(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[:limit]
}

func trimSpace(text string) string {
	start, end := 0, len(text)
	for start < end && isSpace(text[start]) {
		start++
	}
	for end > start && isSpace(text[end-1]) {
		end--
	}
	return text[start:end]
}

func isSpace(character byte) bool {
	switch character {
	case ' ', '\t', '\n', '\r', '\v', '\f':
		return true
	}
	return false
}
