// Package hotkey watches one key combination and reports press and release.
//
// Windows is asked to register the combination, so nothing of ours runs on
// anyone else's keystrokes. The earlier version used a low-level keyboard
// hook -- because RegisterHotKey reports only the press, and push-to-talk
// needs the release too -- and that put our code in the path of every key on
// the machine. On a box that is also encoding video it showed up as typing
// lag in other applications, which is a bad way to pay for a convenience.
// The release is found by polling the key while it is held instead, which
// costs nothing when she is not talking.
//
// Neither approach needs administrator rights. A foot pedal or a Stream Deck
// key works without special handling: both present as ordinary keys.
package hotkey

import (
	"fmt"
	"log/slog"
	"strings"
)

// Error is a hotkey that could not be understood or registered.
type Error struct{ Reason string }

func (e *Error) Error() string { return e.Reason }

// Mode is what one press of the key means.
type Mode int

const (
	// Hold is push to talk: it activates on the press and releases when she
	// lets go. The default, because it is the one that cannot be left on by
	// accident.
	Hold Mode = iota

	// Toggle presses once to start and once to stop.
	Toggle

	// Press fires once per press and never releases. A key bound to a command
	// uses this: starting a stream has nothing to hold.
	Press
)

// Options configure one watched key.
type Options struct {
	// Combination is written the way the config file has always written it,
	// with named keys in angle brackets: <ctrl>+<alt>+<space>.
	Combination string

	Mode Mode

	// OnActivate runs on every press. OnRelease runs when she lets go in Hold,
	// on the second press in Toggle, and never in Press.
	OnActivate func()
	OnRelease  func()
}

// Watcher is a running hotkey.
type Watcher interface {
	Start() error
	Stop()
	Running() bool
	Combination() string
}

// New builds a watcher for one combination.
func New(options Options) (Watcher, error) {
	keys, err := parseCombination(options.Combination)
	if err != nil {
		return nil, err
	}
	return newWatcher(options, keys)
}

// parseCombination turns "<ctrl>+<alt>+<space>" into the virtual key codes to
// watch for.
func parseCombination(combination string) ([]uint32, error) {
	trimmed := strings.TrimSpace(combination)
	if trimmed == "" {
		return nil, &Error{Reason: "the hotkey is empty"}
	}

	codes := []uint32{}
	for _, part := range strings.Split(trimmed, "+") {
		name := strings.ToLower(strings.TrimSpace(part))
		name = strings.TrimSuffix(strings.TrimPrefix(name, "<"), ">")
		if name == "" {
			continue
		}
		code, ok := virtualKey(name)
		if !ok {
			return nil, &Error{Reason: fmt.Sprintf(
				"could not understand the hotkey %q: %q is not a key I know. "+
					"Named keys need angle brackets, for example "+
					"<ctrl>+<alt>+<space> rather than <ctrl>+<alt>+space.",
				combination, part)}
		}
		codes = append(codes, code)
	}
	if len(codes) == 0 {
		return nil, &Error{Reason: fmt.Sprintf("the hotkey %q has no keys in it", combination)}
	}
	return codes, nil
}

// safely runs a callback without letting an error in it kill the hook thread,
// which would silently take the hotkey away for the rest of the stream.
func safely(callback func()) {
	if callback == nil {
		return
	}
	defer func() {
		if problem := recover(); problem != nil {
			slog.Error("hotkey callback panicked", "panic", problem)
		}
	}()
	callback()
}
