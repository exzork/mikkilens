//go:build !windows

package hotkey

// MikkiLens drives OBS on a Windows streaming machine, and a global keyboard
// hook is the one piece with no portable equivalent. Away from Windows the
// hotkey reports that plainly rather than pretending to watch the keyboard:
// the wake word and the settings app both still work.

func virtualKey(name string) (uint32, bool) {
	// Accept any name so a config written on Windows still parses here; the
	// watcher is what refuses, with a message that explains why.
	return 0, name != ""
}

type unsupportedWatcher struct{ combination string }

func newWatcher(options Options, _ []uint32) (Watcher, error) {
	return &unsupportedWatcher{combination: options.Combination}, nil
}

func (u *unsupportedWatcher) Start() error {
	return &Error{Reason: "a global hotkey is only available on Windows"}
}

func (u *unsupportedWatcher) Stop()               {}
func (u *unsupportedWatcher) Running() bool       { return false }
func (u *unsupportedWatcher) Combination() string { return u.combination }
