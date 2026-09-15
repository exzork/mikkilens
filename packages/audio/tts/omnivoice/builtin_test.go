package omnivoice

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/exzork/mikkilens/packages/core/paths"
)

// emptyRoot points the project at a directory with no voices in it.
func emptyRoot(t *testing.T) {
	t.Helper()
	paths.SetRoot(t.TempDir())
	t.Cleanup(func() { paths.SetRoot("") })
}

// A fresh machine has to be able to speak in her voice as soon as the models
// are there, with nothing recorded and no encoder downloaded. That means it
// arrives prepared, with the transcript that makes it clone well.
func TestTheDefaultVoiceShipsReadyToSpeak(t *testing.T) {
	emptyRoot(t)

	if !InstallBuiltin(DefaultVoice) {
		t.Fatalf("InstallBuiltin(%q) = false", DefaultVoice)
	}
	if !Known(DefaultVoice) {
		t.Errorf("%q is not in the voice list after installing it", DefaultVoice)
	}

	info := describe(DefaultVoice)
	if !info.Prepared {
		t.Error("the built-in voice arrives unprepared, so its first sentence waits for the encoder")
	}
	if info.Seconds < 3 || info.Seconds > 10 {
		t.Errorf("the built-in voice is %.2f s, outside the 3 to 10 the model wants", info.Seconds)
	}
	if info.Text == "" {
		t.Error("the built-in voice has no transcript, which is what most decides how well it clones")
	}
}

// Replacing the built-in voice with a new recording under the same name is an
// ordinary thing to do. Starting MikkiLens again must not put the old one back.
func TestABuiltinVoiceNeverOverwritesHers(t *testing.T) {
	emptyRoot(t)

	if err := os.MkdirAll(voiceDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	hers := filepath.Join(voiceDir(), DefaultVoice+".wav")
	if err := os.WriteFile(hers, []byte("her own recording"), 0o644); err != nil {
		t.Fatal(err)
	}

	if !InstallBuiltin(DefaultVoice) {
		t.Error("a voice that is already there is reported missing")
	}
	if raw, _ := os.ReadFile(hers); string(raw) != "her own recording" {
		t.Error("the built-in voice overwrote the recording she saved under its name")
	}
	if exists(preparedPath(DefaultVoice)) {
		t.Error("the built-in codes were written beside her recording, and would be spoken instead of it")
	}
}

// Only what actually ships. A name from a config file must never become a path.
func TestOnlyBuiltinVoicesAreInstalled(t *testing.T) {
	emptyRoot(t)

	for _, name := range []string{"", "budi", "../" + DefaultVoice, DefaultVoice + "/..", "builtin"} {
		if InstallBuiltin(name) {
			t.Errorf("InstallBuiltin(%q) = true", name)
		}
	}
	if entries, err := os.ReadDir(voiceDir()); err == nil && len(entries) > 0 {
		t.Errorf("names that are not built-in voices wrote %d files", len(entries))
	}
}
