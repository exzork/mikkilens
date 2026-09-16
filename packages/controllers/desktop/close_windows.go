//go:build windows

package desktop

import (
	"fmt"
	"path/filepath"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The Win32 side: which windows are open, and asking them to close.
//
// WM_CLOSE is the polite request -- the same thing pressing the X sends -- so
// an application with unsaved work shows its own dialog and stays open. That
// is the whole point: this runs on a machine she is not looking at, and losing
// a document to a voice command would be unforgivable. Nothing here kills a
// process.

var (
	user32                       = windows.NewLazySystemDLL("user32.dll")
	procEnumWindows              = user32.NewProc("EnumWindows")
	procIsWindowVisible          = user32.NewProc("IsWindowVisible")
	procGetWindowTextW           = user32.NewProc("GetWindowTextW")
	procGetWindowThreadProcessId = user32.NewProc("GetWindowThreadProcessId")
	procPostMessageW             = user32.NewProc("PostMessageW")
	procGetWindowLongW           = user32.NewProc("GetWindowLongW")
)

const (
	wmClose = 0x0010

	wsExToolWindow = 0x00000080 // palettes and tray helpers, not applications
)

// gwlExStyle is negative, and Go will not convert a negative constant to the
// uintptr a syscall argument is. A variable converts at run time instead,
// where it sign-extends to what Win32 expects.
var gwlExStyle = int32(-20)

// Open lists the visible application windows, newest first as Windows reports
// them.
func Open() ([]Window, error) {
	var found []Window
	var failure error

	callback := syscall.NewCallback(func(handle uintptr, _ uintptr) uintptr {
		if visible, _, _ := procIsWindowVisible.Call(handle); visible == 0 {
			return 1
		}
		// A tool window is a palette or a tray helper. It belongs to an
		// application rather than being one, and closing it says nothing.
		if style, _, _ := procGetWindowLongW.Call(handle, uintptr(gwlExStyle)); style&wsExToolWindow != 0 {
			return 1
		}

		title := windowTitle(handle)
		if title == "" {
			return 1
		}

		var process uint32
		procGetWindowThreadProcessId.Call(handle, uintptr(unsafe.Pointer(&process)))
		if process == 0 {
			return 1
		}

		name, err := processName(process)
		if err != nil {
			// A process we cannot name is one we cannot decide about, and
			// guessing would mean closing something protected.
			return 1
		}

		found = append(found, Window{
			Handle: handle, Title: title, Process: process, Name: name,
		})
		return 1
	})

	if result, _, err := procEnumWindows.Call(callback, 0); result == 0 {
		failure = fmt.Errorf("could not list the open windows: %v", err)
	}
	return found, failure
}

// AskToClose sends one window the request to close. It returns as soon as the
// message is posted: what the application does with it -- closing, or putting
// up a "save your work?" dialog -- is its own business and takes its own time.
func AskToClose(window Window) error {
	result, _, err := procPostMessageW.Call(window.Handle, uintptr(wmClose), 0, 0)
	if result == 0 {
		return fmt.Errorf("could not ask %s to close: %v", window.Name, err)
	}
	return nil
}

// CloseAll asks every closable window to close and reports what is still open
// afterwards, by application name.
//
// The wait is what makes the answer worth anything: an application takes a
// moment to go, and asking immediately afterwards would report everything as
// still open. What is left after it is genuinely refusing -- almost always
// because it is asking her about unsaved work.
func CloseAll(settle time.Duration) (asked []string, remaining []string, err error) {
	windows, err := Open()
	if err != nil {
		return nil, nil, err
	}

	closable := Closable(windows)
	asked = Names(closable)
	for _, window := range closable {
		// One failure is not the end: the others still deserve asking.
		_ = AskToClose(window)
	}
	if len(closable) == 0 {
		return nil, nil, nil
	}

	time.Sleep(settle)

	left, err := Open()
	if err != nil {
		return asked, nil, err
	}
	return asked, Names(Closable(left)), nil
}

func windowTitle(handle uintptr) string {
	buffer := make([]uint16, 260)
	length, _, _ := procGetWindowTextW.Call(handle,
		uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)))
	if length == 0 {
		return ""
	}
	return syscall.UTF16ToString(buffer[:length])
}

// processName is the executable's file name, which is what the protected list
// is written in terms of.
func processName(id uint32) (string, error) {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, id)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(handle)

	buffer := make([]uint16, windows.MAX_PATH)
	size := uint32(len(buffer))
	if err := windows.QueryFullProcessImageName(handle, 0, &buffer[0], &size); err != nil {
		return "", err
	}
	return filepath.Base(syscall.UTF16ToString(buffer[:size])), nil
}
