//go:build windows

package hotkey

import (
	"runtime"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows registers the combination for us and only tells us when it is
// pressed. Nothing runs on anyone else's keystrokes.
//
// The first version of this used a low-level keyboard hook, because
// RegisterHotKey reports only the press and push-to-talk needs the release
// too. That was the wrong trade: a hook puts our code in the path of every
// keystroke on the machine, and on a box that is also encoding video that
// showed up as typing lag in other applications. Windows will even remove a
// hook that takes too long, so the hotkey would vanish mid-stream.
//
// So the press comes from RegisterHotKey, and the release is found by polling
// the key while it is held. The polling costs nothing when she is not talking,
// which is almost always.

var (
	user32                 = windows.NewLazySystemDLL("user32.dll")
	procRegisterHotKey     = user32.NewProc("RegisterHotKey")
	procUnregisterHotKey   = user32.NewProc("UnregisterHotKey")
	procGetMessageW        = user32.NewProc("GetMessageW")
	procPostThreadMessageW = user32.NewProc("PostThreadMessageW")
	procGetAsyncKeyState   = user32.NewProc("GetAsyncKeyState")
	kernel32               = windows.NewLazySystemDLL("kernel32.dll")
	procGetCurrentThreadId = kernel32.NewProc("GetCurrentThreadId")
)

const (
	wmHotkey = 0x0312
	wmQuit   = 0x0012

	modAlt      = 0x0001
	modControl  = 0x0002
	modShift    = 0x0004
	modWin      = 0x0008
	modNoRepeat = 0x4000 // one message per press, not a storm while held

	hotkeyID = 1

	// releasePoll is how often the key is checked while it is held down. Fast
	// enough that letting go feels immediate, slow enough to be free.
	releasePoll = 15 * time.Millisecond
)

type message struct {
	HWnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      struct{ X, Y int32 }
}

type windowsWatcher struct {
	options   Options
	modifiers uint32
	trigger   uint32 // the one non-modifier key

	mu       sync.Mutex
	running  bool
	threadID uint32
	ready    chan error
	done     chan struct{}

	held    atomic.Bool // an utterance is in progress
	toggled atomic.Bool
}

func newWatcher(options Options, keys []uint32) (Watcher, error) {
	modifiers, trigger, err := split(keys, options.Combination)
	if err != nil {
		return nil, err
	}
	return &windowsWatcher{options: options, modifiers: modifiers, trigger: trigger}, nil
}

// split separates the modifiers from the one key that actually triggers.
//
// Windows can only register a combination that ends in a real key, so a
// modifiers-only hotkey is refused here with an explanation rather than
// failing later with a number.
func split(keys []uint32, combination string) (uint32, uint32, error) {
	var modifiers, trigger uint32
	for _, code := range keys {
		switch code {
		case 0x11, 0xA2, 0xA3: // ctrl, left, right
			modifiers |= modControl
		case 0x12, 0xA4, 0xA5: // alt
			modifiers |= modAlt
		case 0x10, 0xA0, 0xA1: // shift
			modifiers |= modShift
		case 0x5B, 0x5C: // windows key
			modifiers |= modWin
		default:
			if trigger != 0 {
				return 0, 0, &Error{Reason: "the hotkey " + combination +
					" has more than one ordinary key in it; use modifiers plus one key"}
			}
			trigger = code
		}
	}
	if trigger == 0 {
		return 0, 0, &Error{Reason: "the hotkey " + combination +
			" is only modifiers; add a key, for example <ctrl>+<alt>+<space>"}
	}
	return modifiers, trigger, nil
}

func (w *windowsWatcher) Combination() string { return w.options.Combination }

func (w *windowsWatcher) Running() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.running
}

func (w *windowsWatcher) Start() error {
	w.mu.Lock()
	if w.running {
		w.mu.Unlock()
		return nil
	}
	w.ready = make(chan error, 1)
	w.done = make(chan struct{})
	w.mu.Unlock()

	go w.pump()

	if err := <-w.ready; err != nil {
		return err
	}
	w.mu.Lock()
	w.running = true
	w.mu.Unlock()
	return nil
}

func (w *windowsWatcher) Stop() {
	w.mu.Lock()
	if !w.running {
		w.mu.Unlock()
		return
	}
	w.running = false
	threadID, done := w.threadID, w.done
	w.mu.Unlock()

	// Waking the message loop is what lets it unregister and exit cleanly.
	procPostThreadMessageW.Call(uintptr(threadID), wmQuit, 0, 0)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
}

// pump registers the hotkey and runs the message loop that receives it.
func (w *windowsWatcher) pump() {
	// The registration belongs to the thread that made it, so the thread must
	// not move underneath us.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer close(w.done)

	threadID, _, _ := procGetCurrentThreadId.Call()

	ok, _, err := procRegisterHotKey.Call(
		0, hotkeyID, uintptr(w.modifiers|modNoRepeat), uintptr(w.trigger))
	if ok == 0 {
		w.ready <- &Error{Reason: "could not register the hotkey " +
			w.options.Combination + " (another application may already have it): " +
			err.Error()}
		return
	}
	defer procUnregisterHotKey.Call(0, hotkeyID)

	w.mu.Lock()
	w.threadID = uint32(threadID)
	w.mu.Unlock()
	w.ready <- nil

	var msg message
	for {
		result, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		// GetMessage returns 0 on WM_QUIT and -1 on error; either ends the loop.
		if int32(result) <= 0 {
			return
		}
		if msg.Message == wmHotkey {
			w.onPressed()
		}
	}
}

func (w *windowsWatcher) onPressed() {
	if !w.options.PushToTalk {
		// Toggle mode: odd presses start listening, even presses stop.
		if w.toggled.CompareAndSwap(false, true) {
			safely(w.options.OnActivate)
			return
		}
		w.toggled.Store(false)
		safely(w.options.OnRelease)
		return
	}

	// Hold to talk. One utterance at a time: a repeat while she is still
	// holding must not start a second recording.
	if !w.held.CompareAndSwap(false, true) {
		return
	}
	safely(w.options.OnActivate)
	go w.waitForRelease()
}

// waitForRelease watches the key until she lets go.
//
// This is the only polling in the hotkey, and it runs solely while a key is
// held -- a few seconds per command, rather than forever.
func (w *windowsWatcher) waitForRelease() {
	defer w.held.Store(false)

	ticker := time.NewTicker(releasePoll)
	defer ticker.Stop()

	// A safety limit, in case a key event is lost and the key looks stuck
	// down. Recording has its own maximum length anyway.
	deadline := time.After(2 * time.Minute)

	for {
		select {
		case <-deadline:
			safely(w.options.OnRelease)
			return
		case <-ticker.C:
			if !keyIsDown(w.trigger) {
				safely(w.options.OnRelease)
				return
			}
			if !w.Running() {
				return
			}
		}
	}
}

// keyIsDown asks Windows whether a key is held right now. The high bit is the
// one that means "currently down".
func keyIsDown(code uint32) bool {
	state, _, _ := procGetAsyncKeyState.Call(uintptr(code))
	return state&0x8000 != 0
}
