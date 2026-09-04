package wake

import (
	_ "embed"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/exzork/mikkilens/packages/core/paths"
)

// Builtin is the wake word MikkiLens ships with: her own name.
const Builtin = "mikkilens"

// The model itself, inside the executable.
//
// Everything else the wake word needs is downloaded, because the spectrogram
// and embedding stages are 68 MB between them and are identical for every wake
// word in the world. This one is 850 KB and is nobody else's, so it travels
// with the program. That also removes a whole class of failure: there is no
// release asset to go stale against the name in config.toml, and no download
// to fail halfway and leave a wake word that is configured but not there.
//
//go:embed mikkilens.onnx
var builtin []byte

// installBuiltin writes the built-in wake word beside the downloaded models.
//
// Called from Load and from Installed, which are the two places that would
// otherwise report it missing -- one by failing to start, the other by leaving
// it out of the settings list, where a wake word that cannot be chosen is a
// wake word that does not exist.
//
// Deliberately not a sync.Once. It is one stat call on a path that is almost
// always already correct, and doing it every time means a model deleted or
// half-written by something else is repaired rather than missing until restart.
func installBuiltin() {
	target := filepath.Join(paths.ModelsDir(), Builtin+".onnx")
	if info, err := os.Stat(target); err == nil && info.Size() == int64(len(builtin)) {
		return
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		slog.Warn("could not make room for the built-in wake word", "error", err)
		return
	}
	// Written beside the target and renamed, so a reader never sees a partial
	// model: half an ONNX file loads as an error, and the error would be about
	// a corrupt model rather than about a write that was interrupted.
	temporary := target + ".part"
	if err := os.WriteFile(temporary, builtin, 0o644); err != nil {
		slog.Warn("could not write the built-in wake word", "error", err)
		return
	}
	if err := os.Rename(temporary, target); err != nil {
		slog.Warn("could not put the built-in wake word in place", "error", err)
		_ = os.Remove(temporary)
	}
}
