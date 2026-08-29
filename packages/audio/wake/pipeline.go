package wake

import (
	"os"
	"path/filepath"

	"github.com/exzork/mikkilens/packages/core/paths"
)

// The wake word needs ONNX Runtime, and ONNX Runtime is a native library.
//
// It used to be reached through a cgo binding. That binding is gone, because
// cgo cost the whole build a C toolchain -- and an old one silently produces
// an executable Windows refuses to start. Everything else in MikkiLens is
// pure Go now and builds anywhere.
//
// Reaching the runtime without cgo is possible: its public surface is a C ABI
// in a struct of function pointers, and syscall can call those directly. It is
// deliberately not done here, because getting a slot index wrong does not
// return an error -- it calls an arbitrary function pointer and takes the
// process down. Losing the engine mid-stream would take away her voice control
// entirely, which is a far worse failure than not having the trigger word.
//
// So the wake word reports itself unavailable, clearly, at startup. The hotkey
// is unaffected, and it was always the more reliable of the two triggers.
//
// To bring it back, either bind the runtime through cgo again with a current
// toolchain, or verify the OrtApi slot offsets against the onnxruntime version
// being shipped and implement the three-stage pipeline against them. The
// buffering the pipeline needs is described in wake.go.

type pipeline struct{}

func newPipeline(wakeword string) (*pipeline, error) {
	return nil, &Error{Reason: unavailableReason(wakeword)}
}

func (p *pipeline) close() {}
func (p *pipeline) reset() {}

func (p *pipeline) score([]float32) (float64, error) { return 0, nil }

// unavailableReason says what is missing in the order she would fix it, so the
// message is useful rather than merely accurate.
func unavailableReason(wakeword string) string {
	const base = "the wake word is not available in this build; the hotkey still works"

	if _, err := os.Stat(paths.ModelsDir()); err != nil {
		return base
	}
	missing := []string{}
	for _, name := range []string{"onnxruntime.dll", "melspectrogram", "embedding_model", wakeword} {
		if _, err := findModelFile(paths.ModelsDir(), name); err != nil {
			if _, err := os.Stat(filepath.Join(paths.ModelsDir(), name)); err != nil {
				missing = append(missing, name)
			}
		}
	}
	if len(missing) > 0 {
		return base + " (and these are not installed either: " + join(missing) + ")"
	}
	return base
}

func join(values []string) string {
	out := ""
	for index, value := range values {
		if index > 0 {
			out += ", "
		}
		out += value
	}
	return out
}
