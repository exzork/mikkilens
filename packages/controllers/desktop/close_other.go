//go:build !windows

package desktop

import (
	"errors"
	"time"
)

// Everything here is Win32. MikkiLens is built for Windows -- the audio is
// WASAPI and the wake word loads onnxruntime.dll -- and this is the same
// story: the tests build and run anywhere, and closing windows happens where
// there are windows to close.

var errUnsupported = errors.New("closing the open applications is only available on Windows")

// Open reports no windows off Windows.
func Open() ([]Window, error) { return nil, errUnsupported }

// AskToClose does nothing off Windows.
func AskToClose(Window) error { return errUnsupported }

// CloseAll does nothing off Windows.
func CloseAll(time.Duration) (asked []string, remaining []string, err error) {
	return nil, nil, errUnsupported
}
