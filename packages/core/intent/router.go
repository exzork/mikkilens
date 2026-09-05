package intent

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/exzork/mikkilens/packages/core/fuzzy"
	"github.com/exzork/mikkilens/packages/core/i18n"
)

// Two rules shape this file.
//
// First, every outcome is spoken -- matched, unmatched, ambiguous, refused or
// failed -- because an unacknowledged command is indistinguishable from a
// broken app when the only answer you get is a spoken one.
//
// Second, anything that would end a stream or change what viewers see asks
// first, and the answer is matched against the locale's own yes and no words
// rather than against English.

// Priority orders speech. It lives here rather than in the audio package so
// the router can reach it without depending on audio hardware.
type Priority int

// The tiers are the app's own voice first, then anything someone paid for,
// then the chat backlog. Donations sit above chat because a donation arrives
// once and matters; chat is a stream that will still be there in a minute.
const (
	PriorityError    Priority = 0
	PriorityConfirm  Priority = 1
	PriorityResult   Priority = 2
	PriorityDonation Priority = 3
	PriorityChat     Priority = 4
)

// String is the name used in the log page and in tests.
func (p Priority) String() string {
	switch p {
	case PriorityError:
		return "ERROR"
	case PriorityConfirm:
		return "CONFIRM"
	case PriorityResult:
		return "RESULT"
	case PriorityDonation:
		return "DONATION"
	case PriorityChat:
		return "CHAT"
	default:
		return fmt.Sprintf("PRIORITY(%d)", int(p))
	}
}

// Speaker is the part of the speech bus the router needs. Keeping it this
// narrow is what lets the router be tested without any audio hardware.
type Speaker interface {
	Say(text string, priority Priority)
	SayEarcon(text string, priority Priority, earcon string)
}

// Handler runs one command with whatever its phrase captured. Returning an
// error is how a handler reports a failure it could not speak itself; the
// router says it aloud, because a command that fails silently is the one
// failure this app must not have.
type Handler func(slots map[string]string) error

// answerThreshold is how close a spoken word has to be to a locale yes or no
// word before it counts as an answer.
const answerThreshold = 80.0

type pending struct {
	command  string
	slots    map[string]string
	prompt   string
	deadline time.Time
}

// Router dispatches transcripts and gates the destructive commands.
type Router struct {
	mu       sync.Mutex
	commands *Set
	bus      Speaker
	locale   *i18n.Locale
	timeout  time.Duration

	// understander is consulted only when the phrases match nothing.
	understander Understander
	handlers     map[string]Handler
	waiting      *pending
}

// NewRouter wires a command set to a speech bus.
func NewRouter(commands *Set, bus Speaker, locale *i18n.Locale, confirmTimeout time.Duration) *Router {
	if confirmTimeout <= 0 {
		confirmTimeout = 8 * time.Second
	}
	return &Router{
		commands: commands,
		bus:      bus,
		locale:   locale,
		timeout:  confirmTimeout,
		handlers: map[string]Handler{},
	}
}

// Register wires one command id to the code behind it.
func (r *Router) Register(id string, handler Handler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.handlers[id] = handler
}

// RegisterAll wires up a whole group of handlers at once.
func (r *Router) RegisterAll(handlers map[string]Handler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, handler := range handlers {
		r.handlers[id] = handler
	}
}

// Handlers is a copy of the current wiring, so a command reload can put it back.
func (r *Router) Handlers() map[string]Handler {
	r.mu.Lock()
	defer r.mu.Unlock()
	copied := make(map[string]Handler, len(r.handlers))
	for id, handler := range r.handlers {
		copied[id] = handler
	}
	return copied
}

// SetCommands swaps in a new command set, keeping the handlers already wired.
func (r *Router) SetCommands(commands *Set) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.commands = commands
}

// Commands is the command set currently in use.
func (r *Router) Commands() *Set {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.commands
}

// SetLocale switches the language mid-run.
func (r *Router) SetLocale(locale *i18n.Locale) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.locale = locale
}

// SetConfirmTimeout changes how long a confirmation stays open.
func (r *Router) SetConfirmTimeout(timeout time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if timeout > 0 {
		r.timeout = timeout
	}
}

// UnhandledCommands are commands defined in commands.toml with no code behind
// them yet. The settings page shows these so a typo in an id is visible.
func (r *Router) UnhandledCommands() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	missing := []string{}
	for _, id := range r.commands.Order {
		if _, ok := r.handlers[id]; !ok {
			missing = append(missing, id)
		}
	}
	return missing
}

// Understander is a last resort for an utterance the phrases did not match.
//
// An interface rather than the model client itself, so this package stays free
// of HTTP, providers and API keys: matching commands is the core of what
// MikkiLens does and must remain testable without a network.
//
// Returning an empty command means "I did not recognise one", which is a valid
// answer. It is expected to be slow -- a second or so of local inference --
// which is why it is only ever reached once the cheap path has failed.
type Understander interface {
	Understand(ctx context.Context, transcript string, commands *Set) (Resolution, error)
}

// Resolution is what the fallback made of an utterance.
type Resolution struct {
	Command string
	Slots   map[string]string

	// Answered means it has already said everything there is to say, and
	// nothing should be dispatched.
	//
	// This is the shape of a question rather than an order. "Berapa menit lagi
	// sampai jam 12" needs the time to answer but is not a request to be told
	// it, so for commands marked `answers` the fallback runs the command
	// itself, gives the result back to the model, and speaks what comes back.
	// Dispatching afterwards would run the command a second time and say the
	// bare result on top of the answer.
	Answered bool
}

// SetUnderstander installs the fallback. Nil disables it, which restores the
// behaviour of refusing anything the phrases do not match.
func (r *Router) SetUnderstander(understander Understander) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.understander = understander
}

// AwaitingConfirmation reports whether a question is still open.
func (r *Router) AwaitingConfirmation() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.waiting != nil && time.Now().Before(r.waiting.deadline)
}

// CancelPending drops an open question and says so.
func (r *Router) CancelPending() {
	r.mu.Lock()
	had := r.waiting != nil
	r.waiting = nil
	locale := r.locale
	r.mu.Unlock()

	if had {
		r.bus.Say(locale.T("confirm.cancelled"), PriorityConfirm)
	}
}

// RenewPending restarts the clock on an open question.
//
// The deadline is set when the question is queued, but it is meant to measure
// how long she has to answer -- and several seconds of that can be spent
// speaking the question itself. Without this, a long prompt could expire
// before she was ever given the chance to reply, which reads as MikkiLens
// asking something and then refusing to listen.
func (r *Router) RenewPending() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.waiting != nil {
		r.waiting.deadline = time.Now().Add(r.timeout)
	}
}

// TimeOutPending closes an unanswered question and says so.
//
// The deadline alone is not enough: it is only noticed when something else is
// said, so a question nobody answers would otherwise sit open in silence with
// the answer still expected. Saying "no answer, cancelled" out loud is the
// only way she learns the thing did not happen.
func (r *Router) TimeOutPending() {
	r.mu.Lock()
	had := r.waiting != nil
	r.waiting = nil
	locale := r.locale
	r.mu.Unlock()

	if had {
		r.bus.Say(locale.T("confirm.timeout"), PriorityConfirm)
	}
}

// HandleTranscript processes one utterance and returns the command that ran,
// if any. Every path through it speaks.
func (r *Router) HandleTranscript(text string) string {
	r.mu.Lock()
	locale := r.locale
	r.mu.Unlock()

	if trimmed := trimSpace(text); trimmed == "" {
		r.bus.Say(locale.T("listen.no_speech"), PriorityResult)
		return ""
	}

	r.expirePending()

	r.mu.Lock()
	waiting := r.waiting
	commands := r.commands
	r.mu.Unlock()

	if waiting != nil {
		return r.handleAnswer(waiting, text)
	}

	match, rivals := commands.Match(text)
	if len(rivals) > 0 {
		ids := make([]string, 0, len(rivals))
		for _, rival := range rivals {
			ids = append(ids, rival.Command)
		}
		slog.Info("ambiguous transcript", "text", text, "matched", ids)
		r.bus.Say(locale.T("listen.ambiguous", i18n.Args{"phrase": trimSpace(text)}), PriorityResult)
		return ""
	}
	if match == nil {
		understood, answered := r.understand(text)
		if understood != nil {
			return r.dispatch(*understood)
		}
		if answered {
			// Already answered, in its own words. "I do not know that command"
			// on top of it would be talking over something she is listening to
			// and contradicting it besides.
			return ""
		}
		r.bus.Say(locale.T("listen.unknown_command", i18n.Args{"text": trimSpace(text)}), PriorityResult)
		return ""
	}
	return r.dispatch(*match)
}

// understand asks the fallback what an unmatched utterance meant.
//
// Everything it returns is checked against the commands that actually exist.
// A model inventing a plausible-sounding id, or filling in a slot nothing
// understands, must come to nothing rather than to a command that was never
// written -- and the result goes through dispatch like any other match, so a
// command marked confirm still asks before it acts.
// The second return says the fallback has already spoken, which is different
// from it having found nothing: one must stay silent, the other must say so.
func (r *Router) understand(text string) (*Match, bool) {
	r.mu.Lock()
	understander := r.understander
	commands := r.commands
	r.mu.Unlock()

	if understander == nil || commands == nil {
		return nil, false
	}

	ctx, cancel := context.WithTimeout(context.Background(), understandTimeout)
	defer cancel()

	resolved, err := understander.Understand(ctx, text, commands)
	if err != nil {
		// Not spoken: she already hears "I do not know that command", and
		// explaining that a model failed helps nobody mid-stream.
		slog.Warn("the fallback matcher failed", "error", err)
		return nil, false
	}
	if resolved.Answered {
		// Already spoken about, in its own words.
		return nil, true
	}
	id := trimSpace(resolved.Command)
	if id == "" {
		return nil, false
	}
	if _, exists := commands.Commands[id]; !exists {
		slog.Warn("the fallback matcher invented a command", "command", id)
		return nil, false
	}

	kept := map[string]string{}
	for name, value := range resolved.Slots {
		if KnownSlots[name] && trimSpace(value) != "" {
			kept[name] = trimSpace(value)
		}
	}

	slog.Info("understood by the fallback matcher", "text", text, "command", id)
	return &Match{Command: id, Slots: kept, Transcript: text}, false
}

// understandTimeout bounds the whole fallback. The client has its own, shorter
// deadline; this is the backstop that keeps a wedged local server from leaving
// her waiting with no answer at all.
const understandTimeout = 20 * time.Second

func (r *Router) expirePending() {
	r.mu.Lock()
	expired := false
	if r.waiting != nil && !time.Now().Before(r.waiting.deadline) {
		r.waiting = nil
		expired = true
	}
	locale := r.locale
	r.mu.Unlock()

	if expired {
		r.bus.Say(locale.T("confirm.timeout"), PriorityConfirm)
	}
}

func (r *Router) handleAnswer(waiting *pending, text string) string {
	r.mu.Lock()
	locale := r.locale
	r.mu.Unlock()

	verdict, understood := r.classifyAnswer(text)
	if !understood {
		// Deliberately keeps the question open: an unclear answer must not be
		// read as "no", and must not silently do the thing either.
		r.bus.Say(locale.T("confirm.not_understood"), PriorityConfirm)
		return ""
	}

	r.mu.Lock()
	r.waiting = nil
	r.mu.Unlock()

	if !verdict {
		r.bus.Say(locale.T("confirm.cancelled"), PriorityConfirm)
		return ""
	}
	return r.execute(waiting.command, waiting.slots)
}

// classifyAnswer decides yes, no, or neither, using this locale's own words.
func (r *Router) classifyAnswer(text string) (verdict bool, understood bool) {
	r.mu.Lock()
	locale := r.locale
	r.mu.Unlock()

	cleaned := Normalize(text)
	if cleaned == "" {
		return false, false
	}
	first := firstWord(cleaned)

	for _, option := range []struct {
		words   []string
		verdict bool
	}{
		{locale.YesWords(), true},
		{locale.NoWords(), false},
	} {
		normalized := make([]string, 0, len(option.words))
		for _, word := range option.words {
			normalized = append(normalized, Normalize(word))
		}
		if _, score := fuzzy.ExtractOne(first, normalized, fuzzy.Ratio); score >= answerThreshold {
			return option.verdict, true
		}
	}
	return false, false
}

// Trigger runs a command that nobody said.
//
// A key on a Stream Deck, a mouse macro, the settings page and the command
// line all arrive here, and from here on nothing can tell them apart from
// speech: the same handler runs and the same sentence is spoken. That is the
// whole point -- a key that acts silently would be the one way to change her
// stream without her being told about it.
//
// confirm chooses whether the command's own gate applies. A dedicated key is
// a deliberate act in a way that a misheard sentence is not, so a binding is
// allowed to turn it off; leaving it on means the key asks, and she answers
// out loud.
func (r *Router) Trigger(id string, confirm bool) string {
	r.mu.Lock()
	command, known := r.commands.Commands[id]
	locale := r.locale
	r.mu.Unlock()

	if !known {
		slog.Warn("no such command", "command", id)
		r.bus.Say(locale.T("error.not_available", i18n.Args{"command": id}), PriorityError)
		return ""
	}
	if !confirm || !command.Confirm {
		return r.execute(id, nil)
	}
	return r.dispatch(Match{Command: id})
}

func (r *Router) dispatch(match Match) string {
	r.mu.Lock()
	command := r.commands.Commands[match.Command]
	locale := r.locale
	timeout := r.timeout
	r.mu.Unlock()

	if !command.Confirm {
		return r.execute(match.Command, match.Slots)
	}

	prompt := command.ConfirmPrompt
	if prompt == "" {
		prompt = "confirm.cancelled"
	}
	resolved := locale.Resolve(prompt, slotArgs(match.Slots))

	r.mu.Lock()
	r.waiting = &pending{
		command:  match.Command,
		slots:    match.Slots,
		prompt:   resolved,
		deadline: time.Now().Add(timeout),
	}
	r.mu.Unlock()

	r.bus.SayEarcon(resolved, PriorityConfirm, "confirm")
	return ""
}

func (r *Router) execute(id string, slots map[string]string) string {
	r.mu.Lock()
	handler, ok := r.handlers[id]
	locale := r.locale
	r.mu.Unlock()

	if !ok {
		// "That command is not available yet" is far more useful than silence,
		// and far more useful than "I did not understand you".
		slog.Warn("no handler registered", "command", id)
		r.bus.Say(locale.T("error.not_available", i18n.Args{"command": id}), PriorityError)
		return ""
	}

	if err := runHandler(handler, slots); err != nil {
		slog.Error("command failed", "command", id, "error", err)
		r.bus.Say(locale.T("error.generic", i18n.Args{"reason": err.Error()}), PriorityError)
		return ""
	}
	return id
}

// runHandler turns a panicking handler into an error, so one bad command
// cannot take the whole listening loop with it.
func runHandler(handler Handler, slots map[string]string) (err error) {
	defer func() {
		if problem := recover(); problem != nil {
			if failure, ok := problem.(error); ok {
				err = failure
				return
			}
			err = fmt.Errorf("%v", problem)
		}
	}()
	return handler(slots)
}

func slotArgs(slots map[string]string) i18n.Args {
	args := make(i18n.Args, len(slots))
	for name, value := range slots {
		args[name] = value
	}
	return args
}

func firstWord(cleaned string) string {
	for index := 0; index < len(cleaned); index++ {
		if cleaned[index] == ' ' {
			return cleaned[:index]
		}
	}
	return cleaned
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
