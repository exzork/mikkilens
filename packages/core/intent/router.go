package intent

import (
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
// broken app when you cannot see the screen.
//
// Second, anything that would end a stream or change what viewers see asks
// first, and the answer is matched against the locale's own yes and no words
// rather than against English.

// Priority orders speech. It lives here rather than in the audio package so
// the router can reach it without depending on audio hardware.
type Priority int

const (
	PriorityError   Priority = 0
	PriorityConfirm Priority = 1
	PriorityResult  Priority = 2
	PriorityChat    Priority = 3
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
	handlers map[string]Handler
	waiting  *pending
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
		r.bus.Say(locale.T("listen.unknown_command", i18n.Args{"text": trimSpace(text)}), PriorityResult)
		return ""
	}
	return r.dispatch(*match)
}

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
