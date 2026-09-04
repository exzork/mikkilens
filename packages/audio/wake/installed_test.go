package wake_test

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/exzork/mikkilens/packages/audio/wake"
	"github.com/exzork/mikkilens/packages/core/config"
	"github.com/exzork/mikkilens/packages/core/paths"
)

// The settings page offers what Installed returns, so what it leaves out
// matters as much as what it lists: the two shared stages of the pipeline are
// not wake words, and offering them would put a name in the dropdown that
// loads and then never fires.

func modelsIn(t *testing.T, files ...string) {
	t.Helper()

	previous := paths.Root()
	root := t.TempDir()
	models := filepath.Join(root, "data", "models")
	if err := os.MkdirAll(models, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range files {
		if err := os.WriteFile(filepath.Join(models, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	paths.SetRoot(root)
	t.Cleanup(func() { paths.SetRoot(previous) })
}

func TestInstalledListsTheWakeWordsAndNotTheStagesTheyShare(t *testing.T) {
	modelsIn(t,
		"melspectrogram.onnx", "embedding_model.onnx",
		"hey_jarvis_v0.1.onnx", "alexa_v0.1.onnx", "notes.txt")

	// mikkilens is in the list without having been written here: it ships
	// inside the executable, and Installed puts it on disk before it looks.
	want := []string{"alexa", "hey_jarvis", wake.Builtin}
	if got := wake.Installed(); !reflect.DeepEqual(got, want) {
		t.Errorf("Installed() = %v, want %v", got, want)
	}
}

// The name in config.toml is the name without the training run on the end,
// because that is the name findModelFile resolves back to a file.
func TestTheNameOfferedIsTheNameTheConfigFileUses(t *testing.T) {
	for file, want := range map[string]string{
		"hey_jarvis_v0.1.onnx": "hey_jarvis",
		"hey_mycroft.onnx":     "hey_mycroft",
		"timer_v2.onnx":        "timer",
		"hi_miki_v0.1.2.onnx":  "hi_miki",
	} {
		if got := wake.WakeWordName(file); got != want {
			t.Errorf("WakeWordName(%q) = %q, want %q", file, got, want)
		}
	}
}

// With nothing downloaded at all there is still exactly one wake word, because
// MikkiLens carries her own name inside the executable. This is the whole
// point of embedding it: the settings page can never offer an empty list, and
// the name in config.toml can never be one with no model behind it.
func TestTheBuiltInWakeWordIsThereWithNothingDownloaded(t *testing.T) {
	modelsIn(t)

	if got, want := wake.Installed(), []string{wake.Builtin}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Installed() = %v, want %v", got, want)
	}

	written := filepath.Join(paths.ModelsDir(), wake.Builtin+".onnx")
	info, err := os.Stat(written)
	if err != nil {
		t.Fatalf("the built-in wake word was listed but not written: %v", err)
	}
	// An ONNX file, not a stub: a truncated model lists fine and then fails to
	// load, which is the failure this test exists to make impossible.
	if info.Size() < 100_000 {
		t.Errorf("%s is %d bytes, too small to be the model", written, info.Size())
	}
}

// The default in config.toml has to name a model that exists. They are written
// out separately -- config cannot import this package without dragging the
// ONNX runtime and a C toolchain in behind it -- so something has to check.
func TestBuiltinIsTheConfigDefault(t *testing.T) {
	if got := config.Default().Wake.Model; got != wake.Builtin {
		t.Errorf("config defaults to wake word %q, but the built-in one is %q",
			got, wake.Builtin)
	}
}
