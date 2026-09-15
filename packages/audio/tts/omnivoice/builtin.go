package omnivoice

import (
	"embed"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
)

// DefaultVoice is the voice MikkiLens reads in out of the box: hers.
//
// config.Default names it too, spelled out, because that package sits under
// this one; tts.TestTheDefaultIsHerOwnVoice keeps the two honest.
const DefaultVoice = "mikkiru"

// The voices that travel inside the executable.
//
// Everything else OmniVoice needs is downloaded -- two gigabytes of models that
// are identical for everybody. A voice is half a megabyte and belongs to one
// person, so it ships with the program, the way the wake word does. It ships
// already prepared, too: the .json beside the recording is the voice, so a
// fresh machine can speak in it without first fetching the encoder that makes
// one, and without the seconds of work that encoding costs.
//
//go:embed builtin
var builtin embed.FS

// InstallBuiltin puts a voice MikkiLens ships with into the voices folder, and
// reports whether that voice is there afterwards.
//
// A voice she already has under the same name is hers, and is never touched:
// replacing the built-in voice with a new recording is an ordinary thing to
// do, and a restart that quietly put the old one back would undo it. So this
// only ever fills a gap -- the first run, or the voice being removed while it
// is still the one chosen, where bringing it back is the only reading that
// ends with something being said.
//
// A name that is not a built-in voice reports false and writes nothing. Names
// are matched against the embedded files exactly, so nothing a config file
// says can become a path.
func InstallBuiltin(name string) bool {
	files := builtinFiles(name)
	if len(files) == 0 {
		return false
	}
	for _, extension := range []string{".json", ".wav"} {
		if exists(filepath.Join(voiceDir(), name+extension)) {
			return true
		}
	}

	if err := os.MkdirAll(voiceDir(), 0o755); err != nil {
		slog.Warn("could not make room for the built-in voice", "voice", name, "error", err)
		return false
	}
	// The recording and transcript before the prepared file, and each written
	// beside its target and renamed: the .json is what marks a voice as ready,
	// so it lands last and never half-written.
	for _, file := range files {
		raw, err := builtin.ReadFile(path.Join("builtin", file))
		if err != nil {
			slog.Warn("could not read the built-in voice", "file", file, "error", err)
			return false
		}
		target := filepath.Join(voiceDir(), file)
		temporary := target + ".part"
		if err := os.WriteFile(temporary, raw, 0o644); err != nil {
			slog.Warn("could not write the built-in voice", "file", file, "error", err)
			return false
		}
		if err := os.Rename(temporary, target); err != nil {
			slog.Warn("could not put the built-in voice in place", "file", file, "error", err)
			_ = os.Remove(temporary)
			return false
		}
	}
	return true
}

// builtinFiles is what ships for one voice, prepared file last.
func builtinFiles(name string) []string {
	if name == "" {
		return nil
	}
	entries, err := fs.ReadDir(builtin, "builtin")
	if err != nil {
		return nil
	}
	shipped := map[string]bool{}
	for _, entry := range entries {
		shipped[entry.Name()] = true
	}
	var files []string
	for _, extension := range []string{".wav", ".txt", ".json"} {
		if shipped[name+extension] {
			files = append(files, name+extension)
		}
	}
	return files
}
