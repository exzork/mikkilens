// Package feedback is the speech bus: one priority queue that owns the output
// device.
//
// Every subsystem speaks through here, which is what makes audible
// confirmation structural rather than something each feature has to remember.
// The priority ordering encodes one rule -- an error must never wait behind
// forty queued chat messages -- and interrupted speech is put back on the
// queue rather than dropped, so preemption never silently loses a message.
//
// The tiers, in the order they are heard: the app's own voice (errors, open
// questions, command results), then donations, then the chat backlog.
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
	Error    = intent.PriorityError
	Confirm  = intent.PriorityConfirm
	Result   = intent.PriorityResult
	Donation = intent.PriorityDonation
	Chat     = intent.PriorityChat
)

// Utterance is one thing waiting to be said.
type Utterance struct {
	Text     string
	Priority Priority
	Earcon   string
	Voice    string // empty means chosen from the priority and config
	Rate     string
	Volume   string // empty means the configured speech volume

	// RequeueIfInterrupted puts the utterance back at its original place in the
	// queue when something preempts it. Chat sets it, so being cut off by an
	// error means the message is re-read rather than lost.
	RequeueIfInterrupted bool

	// ThroughHold lets this be spoken while the donation hold is on.
	//
	// The donation being announced sets it, and only it. The hold exists so
	// that nothing talks over an alert; the voice reading that alert out is
	// not something to protect the alert from, and holding it would leave the
	// donation announced after it had left the screen.
	ThroughHold bool

	// Group names a burst of utterances that belong together, so they can be
	// called off as one.
	//
	// Reading five search results is seven utterances, and the moment she
	// picks one the other six are wrong -- she is not waiting to hear options
	// four and five over the song she just started. Cancelling by priority
	// would take unrelated answers with it; this takes exactly the burst that
	// has been overtaken.
	Group string

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
	chatHeld   time.Time
	chatMuted  bool
	running    bool
	idle       chan struct{}
	history    []Spoken
	worker     sync.WaitGroup
	stopping   chan struct{}
	settings   config.Config
	locale     *i18n.Locale
	player     Player
	synthesize Synthesizer

	// onSpeaking is told when the speakers start and stop carrying speech, so
	// the wake word can be switched off while MikkiLens is talking: her name is
	// the trigger, and it comes back through the microphone like anyone else
	// saying it.
	onSpeaking func(bool)
}

// OnSpeaking installs the hook. Nil removes it.
func (b *Bus) OnSpeaking(hook func(bool)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.onSpeaking = hook
}

// speakingHook reads it back under the lock.
func (b *Bus) speakingHook() func(bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.onSpeaking
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

// SayDonation queues something someone paid to have read.
//
// It sits above chat and below the app's own voice: a donation preempts the
// chat backlog, so it is heard while it is still current rather than twenty
// messages later, but it never talks over an error or an open question.
//
// Like chat, it is re-read rather than lost when something preempts it --
// more so, since dropping a message someone paid for is the one failure here
// that costs the streamer something.
func (b *Bus) SayDonation(text string, onSpoken func(bool)) {
	settings, locale := b.Config(), b.Locale()
	b.Enqueue(Utterance{
		Text:                 text,
		Priority:             Donation,
		Earcon:               "donation",
		ThroughHold:          true,
		Voice:                settings.VoiceForDonation(locale.DefaultVoice()),
		Rate:                 settings.Speech.DonationRate,
		Volume:               settings.Speech.DonationVolume,
		RequeueIfInterrupted: true,
		OnSpoken:             onSpoken,
	})
}

// SayChat queues a chat message. Interrupted messages are re-read, not lost.
//
// Ordinary messages get no tone. One beep is an acknowledgement; a beep before
// every message for an hour is a metronome, and it doubles the time chat takes
// to get through. The voice already says who is speaking, which is the part
// that actually needed marking.
//
// Super chats keep theirs. They are rare, and someone paying to be heard is
// worth distinguishing from the stream of ordinary messages.
func (b *Bus) SayChat(text string, paid bool, onSpoken func(bool)) {
	settings, locale := b.Config(), b.Locale()

	priority, earcon := Chat, ""
	if paid {
		// The same tier a Tako or Trakteer alert holds chat for, because it is
		// the same thing: somebody paid, and it is worth hearing while it is
		// still current rather than twenty messages later.
		//
		// The voice stays the chat voice. It is still chat, still her audience
		// talking, and the tone is what marks it out.
		priority, earcon = Donation, "superchat"
	}

	b.Enqueue(Utterance{
		Text:                 text,
		Priority:             priority,
		Earcon:               earcon,
		Voice:                settings.VoiceForChat(locale.DefaultVoice()),
		Rate:                 settings.Speech.ChatRate,
		Volume:               settings.Speech.ChatVolume,
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

// ClearGroup drops everything pending in one group and cuts off the member
// being spoken right now. It reports how many were dropped.
//
// The interruption is the point rather than a side effect. She presses 2 while
// result three is being read, and what has to happen is that the voice stops
// mid-word and the song starts -- not that she listens to the rest of a list
// she has already chosen from.
//
// Nothing is requeued: these are called off, not postponed, and a group member
// sets no requeue flag precisely so that being cut off here is final.
func (b *Bus) ClearGroup(group string) int {
	if group == "" {
		return 0
	}

	b.mu.Lock()
	before := len(b.queue)
	kept := b.queue[:0]
	for _, entry := range b.queue {
		if entry.what.Group != group {
			kept = append(kept, entry)
		}
	}
	b.queue = kept
	heap.Init(&b.queue)

	interrupt := b.current != nil && b.current.Group == group
	if len(b.queue) == 0 && b.current == nil {
		b.markIdleLocked()
	}
	b.cond.Broadcast()
	dropped := before - len(b.queue)
	b.mu.Unlock()

	if interrupt {
		b.player.Stop()
	}
	return dropped
}

// SpeakingGroup is the group of the utterance being spoken, or "" for none.
//
// The key that picks a song asks this: cancelling costs nothing when the
// reading has already finished, but knowing whether it was still going is what
// decides whether she is cut off mid-sentence or simply answered.
func (b *Bus) SpeakingGroup() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.current == nil {
		return ""
	}
	return b.current.Group
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

// -- the mute -----------------------------------------------------------------

// SetChatMuted holds chat back until it is turned off again, and cuts off a
// message being read right now.
//
// Held, not dropped, and that is the whole design. Something happens in the
// room -- a guest arrives, someone speaks to her, a song starts -- and what
// she needs is for the voice in her ear to stop this second. What she does not
// need is to have paid for that with the twenty messages that arrived while it
// was quiet. Muting silences; unmuting reads the backlog, oldest first, exactly
// as pausing the reader does.
//
// It is deliberately not the donation hold with a long deadline. The hold is a
// few seconds of getting out of an alert's way and it expires on its own; this
// stays until she says otherwise, and the two have to be able to be true at
// once without either cancelling the other.
//
// Only chat and the paid tier are muted. An error, a confirmation, or an answer
// she asked for still speaks straight through -- those are about her, not about
// the stream, and a mute that swallowed "OBS is not responding" would be a way
// to go off the air quietly.
func (b *Bus) SetChatMuted(muted bool) {
	b.mu.Lock()
	if b.chatMuted == muted {
		b.mu.Unlock()
		return
	}
	b.chatMuted = muted
	// Requeued by the worker and then kept there by the gate, so this
	// interrupts the message rather than losing it.
	interrupt := muted && b.current != nil && heldByHold(*b.current)
	b.cond.Broadcast()
	b.mu.Unlock()

	if interrupt {
		b.player.Stop()
	}
}

// ToggleChatMuted flips the mute and reports what it is now, which is what one
// key has to do: there is no second key for the other direction, and she
// cannot see which way it is set.
func (b *Bus) ToggleChatMuted() bool {
	b.mu.Lock()
	muted := !b.chatMuted
	b.mu.Unlock()

	b.SetChatMuted(muted)
	return muted
}

// ChatMuted reports whether chat is being held back by the mute.
func (b *Bus) ChatMuted() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.chatMuted
}

// -- the donation hold --------------------------------------------------------

// HoldChat keeps chat messages queued but unspoken until the deadline passes,
// and cuts off a chat message already being read.
//
// A donation alert comes up on screen with its own voice over it, and two
// voices at once is the one thing worse than chat being read late. Holding
// rather than dropping is what makes it safe to cut a message off mid-word:
// the interrupted message goes back on the queue at its original place and is
// read from the start once the alert is over, so nothing anyone said is lost
// to a donation landing on top of it.
//
// Only chat is held. A microphone failure, or an answer she asked for, still
// speaks straight through an alert -- those are about her, not about the
// stream, and silencing them would leave her waiting on a reply that never
// comes.
func (b *Bus) HoldChat(until time.Time) {
	b.mu.Lock()
	if !until.After(b.chatHeld) {
		// Already held at least this long. Nothing to extend, and no second
		// waker to start.
		held := b.chatHeld
		b.mu.Unlock()
		slog.Debug("chat already held", "until", held)
		return
	}
	b.chatHeld = until
	// Requeued by the worker, and then kept there by the gate above, so this
	// interruption postpones the message rather than restarting it under the
	// alert.
	interrupt := b.current != nil && heldByHold(*b.current)
	b.mu.Unlock()

	if interrupt {
		b.player.Stop()
	}
	go b.wakeAfterHold(until)
}

// ReleaseChat lifts the hold early.
func (b *Bus) ReleaseChat() {
	b.mu.Lock()
	b.chatHeld = time.Time{}
	b.cond.Broadcast()
	b.mu.Unlock()
}

// ChatHeld reports whether chat is being held back, and until when.
func (b *Bus) ChatHeld() (bool, time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return time.Now().Before(b.chatHeld), b.chatHeld
}

// wakeAfterHold nudges the worker once the hold has expired. Without it a held
// queue would sit there until the next thing was enqueued, which on a quiet
// chat could be minutes after the alert finished.
func (b *Bus) wakeAfterHold(until time.Time) {
	timer := time.NewTimer(time.Until(until))
	defer timer.Stop()

	select {
	case <-timer.C:
	case <-b.stopping:
	}
	b.mu.Lock()
	b.cond.Broadcast()
	b.mu.Unlock()
}

// chatBlockedLocked reports whether the next thing to say is a chat message
// that the hold is keeping back.
//
// Testing only the front of the queue is what keeps the hold from blocking
// anything else: chat is the lowest priority, so if a chat message is at the
// front then there is nothing more important waiting behind it.

// heldByHold reports whether the donation hold covers this utterance.
//
// What the audience is owed, as against what she is owed. Super chats sit in
// the donation tier now, and a super chat read over a Tako or Trakteer alert
// would be two paid messages at once -- the exact thing the hold exists to
// prevent.
func heldByHold(utterance Utterance) bool {
	if utterance.ThroughHold {
		return false
	}
	return utterance.Priority == Chat || utterance.Priority == Donation
}
func (b *Bus) chatBlockedLocked() bool {
	if len(b.queue) == 0 {
		return false
	}
	if !b.chatMuted && !time.Now().Before(b.chatHeld) {
		return false
	}
	return heldByHold(b.queue[0].what)
}

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
		for b.running && (len(b.queue) == 0 || b.chatBlockedLocked()) {
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
	volume := utterance.Volume
	if volume == "" {
		volume = settings.Speech.Volume
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	audio, err := b.synthesize(ctx, utterance.Text, tts.Options{
		Voice: voice, Rate: rate, Volume: volume,
		NoCache: utterance.Priority == Chat || utterance.Priority == Donation,
	})
	if err != nil {
		slog.Error("could not synthesize speech", "text", clip(utterance.Text, 60), "error", err)
		b.Earcon("error")
		// Not requeued: retrying would fail in exactly the same way, and a
		// message that never stops being retried blocks everything behind it.
		return true
	}

	// A hold that arrived while this was being synthesized has to be caught
	// here, because interrupting cannot catch it: the player is told to stop
	// before it has started, and starting clears that. Chat is always
	// synthesized fresh, so there is about a second of this window on every
	// message, and a donation landing in it would otherwise be talked over --
	// which is the whole thing the hold exists to prevent.
	//
	// Reported as not completed, so it goes back on the queue at its original
	// place and is read from the start once the alert is over, exactly as an
	// interrupted one is.
	if heldByHold(utterance) {
		if held, _ := b.ChatHeld(); held || b.ChatMuted() {
			return false
		}
	}

	// Announced around the playback itself rather than around the whole
	// utterance: synthesis happens first and can take a second, and the
	// microphone has nothing to fear from it.
	if hook := b.speakingHook(); hook != nil {
		hook(true)
		defer hook(false)
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
	case Donation:
		return "donation"
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
