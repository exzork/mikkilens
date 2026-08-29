//go:build windows

package hotkey

import (
	"runtime"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The hook has to live on one OS thread with a message pump, because Windows
// delivers low-level keyboard events by posting to that thread's queue.
// runtime.LockOSThread is what keeps the Go scheduler from moving it.

var (
	user32                  = windows.NewLazySystemDLL("user32.dll")
	procSetWindowsHookExW   = user32.NewProc("SetWindowsHookExW")
	procUnhookWindowsHookEx = user32.NewProc("UnhookWindowsHookEx")
	procCallNextHookEx      = user32.NewProc("CallNextHookEx")
	procGetMessageW         = user32.NewProc("GetMessageW")
	procPostThreadMessageW  = user32.NewProc("PostThreadMessageW")
	kernel32                = windows.NewLazySystemDLL("kernel32.dll")
	procGetCurrentThreadId  = kernel32.NewProc("GetCurrentThreadId")
)

const (
	whKeyboardLL = 13
	wmKeyDown    = 0x0100
	wmKeyUp      = 0x0101
	wmSysKeyDown = 0x0104
	wmSysKeyUp   = 0x0105
	wmQuit       = 0x0012
)

type keyboardLLHookStruct struct {
	VkCode    uint32
	ScanCode  uint32
	Flags     uint32
	Time      uint32
	ExtraInfo uintptr
}

type message struct {
	HWnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      struct{ X, Y int32 }
}

type windowsWatcher struct {
	options Options
	wanted  map[uint32]bool

	mu        sync.Mutex
	pressed   map[uint32]bool
	active    bool
	toggledOn bool
	running   bool
	threadID  uint32
	ready     chan error
	done      chan struct{}
	hook      uintptr

	// events carries callbacks off the hook thread. Windows silently removes a
	// low-level hook that takes too long to return (300 ms by default), which
	// would take the hotkey away mid-stream with nothing to explain it -- so
	// the hook only ever enqueues, and a separate goroutine does the work.
	events chan func()
}

func newWatcher(options Options, keys []uint32) (Watcher, error) {
	wanted := make(map[uint32]bool, len(keys))
	for _, code := range keys {
		wanted[code] = true
	}
	return &windowsWatcher{
		options: options,
		wanted:  wanted,
		pressed: map[uint32]bool{},
	}, nil
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
	w.events = make(chan func(), 32)
	events := w.events
	w.mu.Unlock()

	go w.pump()
	go func() {
		for handle := range events {
			handle()
		}
	}()

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
	threadID := w.threadID
	done := w.done
	events := w.events
	w.events = nil
	w.pressed = map[uint32]bool{}
	w.active = false
	w.mu.Unlock()

	// Waking the message loop is what lets it unhook and exit cleanly.
	procPostThreadMessageW.Call(uintptr(threadID), wmQuit, 0, 0)
	<-done
	if events != nil {
		close(events)
	}
}

// dispatch hands a callback to the worker goroutine. A full queue is dropped
// rather than blocking: stalling here would stall every keystroke on the
// machine, and one missed trigger beats a frozen keyboard.
func (w *windowsWatcher) dispatch(callback func()) {
	w.mu.Lock()
	events := w.events
	w.mu.Unlock()
	if events == nil {
		return
	}
	select {
	case events <- callback:
	default:
	}
}

// pump installs the hook and runs the message loop that feeds it.
func (w *windowsWatcher) pump() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer close(w.done)

	threadID, _, _ := procGetCurrentThreadId.Call()

	// Taking the event as a typed pointer rather than a uintptr keeps this
	// honest: Windows really does pass a pointer here, and converting back from
	// an integer would be the unsafe version of the same thing.
	callback := windows.NewCallback(func(code int32, wParam uintptr, event *keyboardLLHookStruct) uintptr {
		if code >= 0 && event != nil {
			switch wParam {
			case wmKeyDown, wmSysKeyDown:
				w.onPress(event.VkCode)
			case wmKeyUp, wmSysKeyUp:
				w.onRelease(event.VkCode)
			}
		}
		next, _, _ := procCallNextHookEx.Call(
			0, uintptr(code), wParam, uintptr(unsafe.Pointer(event)))
		return next
	})

	hook, _, err := procSetWindowsHookExW.Call(whKeyboardLL, callback, 0, 0)
	if hook == 0 {
		w.ready <- &Error{Reason: "could not watch the keyboard: " + err.Error()}
		return
	}

	w.mu.Lock()
	w.hook = hook
	w.threadID = uint32(threadID)
	w.mu.Unlock()
	w.ready <- nil

	defer procUnhookWindowsHookEx.Call(hook)

	var msg message
	for {
		result, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		// GetMessage returns 0 on WM_QUIT and -1 on error; either ends the loop.
		if int32(result) <= 0 {
			return
		}
	}
}

func (w *windowsWatcher) onPress(code uint32) {
	if !w.wanted[code] {
		return
	}
	w.mu.Lock()
	w.pressed[code] = true
	fire := w.allHeldLocked() && !w.active
	if fire {
		w.active = true
	}
	w.mu.Unlock()

	if fire {
		w.dispatch(w.fireActivate)
	}
}

func (w *windowsWatcher) onRelease(code uint32) {
	if !w.wanted[code] {
		return
	}
	w.mu.Lock()
	delete(w.pressed, code)
	fire := w.active && !w.allHeldLocked()
	if fire {
		w.active = false
		fire = w.options.PushToTalk
	}
	w.mu.Unlock()

	if fire {
		w.dispatch(func() { safely(w.options.OnRelease) })
	}
}

func (w *windowsWatcher) allHeldLocked() bool {
	for code := range w.wanted {
		if !w.pressed[code] {
			return false
		}
	}
	return true
}

func (w *windowsWatcher) fireActivate() {
	if w.options.PushToTalk {
		safely(w.options.OnActivate)
		return
	}
	// Toggle mode: odd presses start listening, even presses stop.
	w.mu.Lock()
	w.toggledOn = !w.toggledOn
	on := w.toggledOn
	w.mu.Unlock()

	if on {
		safely(w.options.OnActivate)
		return
	}
	safely(w.options.OnRelease)
}
