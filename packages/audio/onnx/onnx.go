// Package onnx loads the ONNX Runtime shared library, once, for everything in
// the process that needs it.
//
// Two subsystems now run models: the wake word listens through three small
// ones, and the local voice speaks through four large ones. The runtime itself
// is a single global -- initializing it twice is not a thing the C API allows
// -- so finding the library and starting it lives here rather than in either
// of them, and whichever asks first pays for it.
//
// It is a separate download rather than something linked in, because the build
// that suits her machine (CPU, CUDA, DirectML) is her choice, and shipping one
// would be shipping the wrong one.
package onnx

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	ort "github.com/yalue/onnxruntime_go"

	"github.com/exzork/mikkilens/packages/core/paths"
)

// RuntimeVersion is the ONNX Runtime this build needs.
//
// It must match the ORT C API version onnxruntime_go was built against --
// ORT_API_VERSION 29 in v1.35.0, which is runtime 1.29. An older library loads
// and then refuses at startup with "Error setting ORT API base", which reads
// as a broken feature rather than as a version that needs bumping alongside
// the Go dependency.
//
// It is declared here, next to the code that loads the library, rather than
// next to the code that downloads it: the version is a property of what we
// link against, and the downloader is only one of the things that needs to
// know it.
const RuntimeVersion = "1.29.0"

// Error is a runtime failure worth reporting aloud.
type Error struct{ Reason string }

func (e *Error) Error() string { return e.Reason }

var (
	startOnce sync.Once
	startErr  error
)

// Start loads the library and initializes the runtime. It is idempotent and
// safe from any goroutine; every caller after the first gets the same answer.
func Start() error {
	startOnce.Do(func() {
		if ort.IsInitialized() {
			return
		}
		library, err := findLibrary()
		if err != nil {
			startErr = err
			return
		}
		ort.SetSharedLibraryPath(library)
		if err := ort.InitializeEnvironment(); err != nil {
			startErr = &Error{Reason: startupReason(library, err)}
		}
	})
	return startErr
}

// Available reports whether models can run at all, for the callers that would
// rather choose something else than fail.
func Available() bool { return Start() == nil }

// startupReason turns the runtime's own refusal into something worth hearing.
//
// Said as-is, a version mismatch names neither the file at fault nor anything
// to do about it, which when the message only ever arrives by ear is the
// difference between a fixable problem and a feature that has simply stopped
// working.
//
// The file is named rather than deleted here. Removing something of hers on
// her behalf, at startup, because a library disagreed about a version number,
// is not a decision this code should be making on its own.
func startupReason(library string, err error) string {
	reason := "the ONNX runtime could not start: " + err.Error()
	if !strings.Contains(err.Error(), "ORT API base") {
		return reason
	}
	return fmt.Sprintf("%s in %s is not the version MikkiLens needs (%s). "+
		"Delete it and start again to fetch the right one.",
		filepath.Base(library), filepath.Dir(library), RuntimeVersion)
}

// Found reports whether the shared library is where it needs to be, without
// loading it.
//
// The settings page asks before it offers the wake-word list, so an empty list
// is explained as "the runtime is missing" rather than shown as a dropdown
// with nothing in it.
func Found() error {
	_, err := findLibrary()
	return err
}

func findLibrary() (string, error) {
	names := []string{"onnxruntime.dll", "libonnxruntime.so", "libonnxruntime.dylib"}
	directories := []string{
		paths.ModelsDir(),
		filepath.Join(paths.ModelsDir(), "onnxruntime"),
		filepath.Join(paths.Root(), "vendor", "onnxruntime"),
		paths.Root(),
	}
	for _, directory := range directories {
		for _, name := range names {
			candidate := filepath.Join(directory, name)
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				return candidate, nil
			}
		}
	}
	return "", &Error{Reason: "onnxruntime.dll was not found; put it in data/models"}
}

// Options keep the runtime from taking the whole machine.
//
// By default ONNX Runtime sizes a thread pool to the core count for every
// session, and those threads spin rather than sleep while waiting for work.
// Several sessions of that pegged every core on this machine and made typing
// lag in other applications -- on a box that is also encoding video, which is
// the one thing MikkiLens must never disturb.
//
// threads is how many the model in hand is worth. One is right for the wake
// word, whose models are tiny and score a chunk in about six milliseconds
// against the eighty milliseconds of audio it represents; there is nothing for
// a pool to do but burn power. The voice is the other case, where the work is
// real and a second core roughly halves the wait.
func Options(threads int) (*ort.SessionOptions, error) {
	if threads < 1 {
		threads = 1
	}
	options, err := ort.NewSessionOptions()
	if err != nil {
		return nil, &Error{Reason: "could not configure the ONNX runtime: " + err.Error()}
	}

	failed := func(err error) (*ort.SessionOptions, error) {
		options.Destroy()
		return nil, &Error{Reason: "could not configure the ONNX runtime: " + err.Error()}
	}

	if err := options.SetIntraOpNumThreads(threads); err != nil {
		return failed(err)
	}
	if err := options.SetInterOpNumThreads(1); err != nil {
		return failed(err)
	}
	if err := options.SetExecutionMode(ort.ExecutionModeSequential); err != nil {
		return failed(err)
	}

	// Spinning is what actually burns the cores. It is a performance knob for
	// servers running back-to-back batches, and the opposite of what a
	// background listener wants.
	for key, value := range map[string]string{
		"session.intra_op.allow_spinning": "0",
		"session.inter_op.allow_spinning": "0",
	} {
		if err := options.AddSessionConfigEntry(key, value); err != nil {
			// An older runtime may not know the key. Not worth failing over:
			// the thread limits above already do most of the work.
			slog.Debug("the ONNX runtime did not accept a setting", "key", key, "error", err)
		}
	}
	return options, nil
}
