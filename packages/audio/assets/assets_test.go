package assets

import (
	"archive/zip"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// contain points the package at a temporary directory, so a test never looks
// at -- or writes to -- the models the machine running it actually uses.
func contain(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	previousDir, previousDriver := modelsDir, graphicsDriver
	modelsDir = func() string { return directory }
	// Off by default: whether the GPU build is wanted is a property of the
	// machine, and a test that changes answer depending on whose laptop it
	// runs on is worse than no test.
	graphicsDriver = func() bool { return false }
	t.Cleanup(func() { modelsDir, graphicsDriver = previousDir, previousDriver })
	return directory
}

// withGraphicsDriver pretends this machine has a driver.
func withGraphicsDriver(t *testing.T) {
	t.Helper()
	previous := graphicsDriver
	graphicsDriver = func() bool { return true }
	t.Cleanup(func() { graphicsDriver = previous })
}

func write(t *testing.T, path string, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestMissingWantsTheEngineAndModelOnAnEmptyMachine(t *testing.T) {
	contain(t)

	wanted := Missing(true, false, "small")

	if !wanted.Has(StageEngine) || !wanted.Has(StageModel) {
		t.Fatalf("a machine with nothing on it should want both, got %v", wanted.Stages)
	}
	if wanted.Stages[0] != StageEngine {
		t.Fatalf("the engine should come first, so a half-finished download still "+
			"leaves something runnable; got %v", wanted.Stages)
	}
	if wanted.Bytes == 0 {
		t.Fatal("the size is what she is told before agreeing to wait for it")
	}
}

func TestMissingSkipsWhatIsAlreadyThere(t *testing.T) {
	directory := contain(t)
	write(t, filepath.Join(directory, "whisper-server.exe"), "binary")
	write(t, filepath.Join(directory, "ggml-small.bin"), "model")

	if wanted := Missing(true, false, "small"); !wanted.Empty() {
		t.Fatalf("nothing should be wanted, got %v", wanted.Stages)
	}
}

// A GPU build already in place is the common case for someone who dropped one
// in by hand, and re-fetching two thirds of a gigabyte over it would be a
// remarkable way to spend their evening.
func TestMissingAcceptsAGPUBuildInItsOwnDirectory(t *testing.T) {
	directory := contain(t)
	write(t, filepath.Join(directory, "whisper", "whisper-server.exe"), "binary")
	write(t, filepath.Join(directory, "whisper", "ggml-small.bin"), "model")

	if wanted := Missing(true, false, "small"); !wanted.Empty() {
		t.Fatalf("a build under whisper/ counts, got %v", wanted.Stages)
	}
}

func TestMissingHonoursTheModelSize(t *testing.T) {
	directory := contain(t)
	write(t, filepath.Join(directory, "whisper-cli.exe"), "binary")
	write(t, filepath.Join(directory, "ggml-base.bin"), "model")

	if wanted := Missing(true, false, "small"); !wanted.Has(StageModel) {
		t.Fatal("base being present says nothing about small")
	}
	if wanted := Missing(true, false, "base"); !wanted.Empty() {
		t.Fatalf("base is what was asked for and it is here, got %v", wanted.Stages)
	}
}

// An empty file is what an interrupted download leaves behind, and treating it
// as a model would mean recognition failing at the moment she speaks rather
// than at startup where it can be fixed.
func TestMissingIgnoresAnEmptyFile(t *testing.T) {
	directory := contain(t)
	write(t, filepath.Join(directory, "whisper-cli.exe"), "binary")
	write(t, filepath.Join(directory, "ggml-small.bin"), "")

	if wanted := Missing(true, false, "small"); !wanted.Has(StageModel) {
		t.Fatal("a zero byte model is not a model")
	}
}

// The graphics build is an upgrade to something already working, so it comes
// last -- and only where there is a driver that could run it.
func TestMissingWantsTheGraphicsBuildLastAndOnlyWithADriver(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the graphics build is only fetched automatically on Windows")
	}
	directory := contain(t)
	withGraphicsDriver(t)
	write(t, filepath.Join(directory, "whisper-cli.exe"), "binary")
	write(t, filepath.Join(directory, "ggml-small.bin"), "model")

	wanted := Missing(true, false, "small")
	if len(wanted.Stages) != 1 || wanted.Stages[0] != StageGPU {
		t.Fatalf("only the upgrade should be left, got %v", wanted.Stages)
	}

	write(t, filepath.Join(directory, "whisper", "whisper-server.exe"), "gpu build")
	if wanted := Missing(true, false, "small"); !wanted.Empty() {
		t.Fatalf("a GPU build is already here, got %v", wanted.Stages)
	}
}

func TestMissingLeavesTheWakeWordAloneWhenItIsOff(t *testing.T) {
	directory := contain(t)
	write(t, filepath.Join(directory, "whisper-cli.exe"), "binary")
	write(t, filepath.Join(directory, "ggml-small.bin"), "model")

	if wanted := Missing(true, false, "small"); wanted.Has(StageWake) {
		t.Fatal("nobody should wait on files for a feature they switched off")
	}
	if wanted := Missing(true, true, "small"); !wanted.Has(StageWake) {
		t.Fatal("the wake word is on and its files are absent")
	}
}

func TestMissingWantsNoSpeechFilesForARemoteEndpoint(t *testing.T) {
	contain(t)

	if wanted := Missing(false, false, "small"); !wanted.Empty() {
		t.Fatalf("recognition happening elsewhere needs none of this, got %v", wanted.Stages)
	}
}

// -- downloading --------------------------------------------------------------

func TestDownloadWritesOnlyOnceItIsWhole(t *testing.T) {
	directory := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("the whole file"))
	}))
	defer server.Close()

	target := filepath.Join(directory, "model.bin")
	if err := download(context.Background(), server.URL, target, 0, nil); err != nil {
		t.Fatal(err)
	}

	contents, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "the whole file" {
		t.Fatalf("got %q", contents)
	}
	if _, err := os.Stat(target + ".part"); !os.IsNotExist(err) {
		t.Fatal("the partial file should be gone once the download finished")
	}
}

// Half a gigabyte over a home connection gets interrupted. Starting again from
// nothing every time would put it out of reach on a slow line.
func TestDownloadResumesFromWhatItAlreadyHas(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "model.bin")
	write(t, target+".part", "first half ")

	var ranged string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ranged = r.Header.Get("Range")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("second half"))
	}))
	defer server.Close()

	if err := download(context.Background(), server.URL, target, 0, nil); err != nil {
		t.Fatal(err)
	}
	if ranged != "bytes=11-" {
		t.Fatalf("should have asked for the rest, asked %q", ranged)
	}

	contents, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "first half second half" {
		t.Fatalf("got %q", contents)
	}
}

// A server that ignores the range header sends the whole file from the start,
// and appending that to a partial one would produce a corrupt model that looks
// perfectly fine on disk.
func TestDownloadStartsOverWhenTheServerIgnoresTheRange(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "model.bin")
	write(t, target+".part", "stale")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("everything"))
	}))
	defer server.Close()

	if err := download(context.Background(), server.URL, target, 0, nil); err != nil {
		t.Fatal(err)
	}

	contents, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "everything" {
		t.Fatalf("got %q", contents)
	}
}

func TestDownloadReportsAFailedRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	target := filepath.Join(t.TempDir(), "model.bin")
	if err := download(context.Background(), server.URL, target, 0, nil); err == nil {
		t.Fatal("a 404 is not a model")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("nothing should have been left behind")
	}
}

// -- unpacking ----------------------------------------------------------------

func makeZip(t *testing.T, path string, entries map[string]string) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	writer := zip.NewWriter(file)
	for name, contents := range entries {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(contents)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
}

// whisper.cpp ships its binaries under Release/ and the ONNX runtime ships its
// library under lib/, while everything looking for them looks in one flat
// directory.
func TestUnpackFlattensAndFilters(t *testing.T) {
	directory := t.TempDir()
	archive := filepath.Join(directory, "build.zip")
	makeZip(t, archive, map[string]string{
		"Release/whisper-server.exe": "server",
		"Release/whisper.dll":        "library",
		"Release/test-vad.exe":       "a test binary nobody needs",
		"Release/wchess.exe":         "an unrelated tool",
	})

	target := filepath.Join(directory, "models")
	if err := unpack(archive, target, wantedFromWhisper); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(target, "whisper-server.exe")); err != nil {
		t.Fatal("the server should have been flattened out of Release/")
	}
	if _, err := os.Stat(filepath.Join(target, "whisper.dll")); err != nil {
		t.Fatal("the libraries it loads have to come with it")
	}
	for _, unwanted := range []string{"test-vad.exe", "wchess.exe"} {
		if _, err := os.Stat(filepath.Join(target, unwanted)); !os.IsNotExist(err) {
			t.Fatalf("%s has no business sitting in data/models forever", unwanted)
		}
	}
}

// The runtime archive is 65 megabytes around one file the wake word loads.
func TestUnpackTakesOnlyTheRuntimeLibrary(t *testing.T) {
	directory := t.TempDir()
	archive := filepath.Join(directory, "runtime.zip")
	makeZip(t, archive, map[string]string{
		"onnxruntime-win-x64/lib/onnxruntime.dll":                  "the library",
		"onnxruntime-win-x64/lib/onnxruntime.lib":                  "an import library",
		"onnxruntime-win-x64/include/onnxruntime_c_api.h":          "a header",
		"onnxruntime-win-x64/lib/onnxruntime_providers_shared.dll": "a provider",
	})

	target := filepath.Join(directory, "models")
	if err := unpack(archive, target, wantedFromRuntime); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(target)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		names := []string{}
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("expected the two libraries, got %s", strings.Join(names, ", "))
	}
}

// An archive that changed shape under a pinned release should say so, not
// leave an empty directory that looks like a successful install.
func TestUnpackReportsAnArchiveHoldingNothingWanted(t *testing.T) {
	directory := t.TempDir()
	archive := filepath.Join(directory, "build.zip")
	makeZip(t, archive, map[string]string{"README.md": "nothing useful here"})

	err := unpack(archive, filepath.Join(directory, "models"), wantedFromWhisper)
	if err == nil {
		t.Fatal("an archive with none of the wanted files is a failure worth saying")
	}
}

// -- the installer ------------------------------------------------------------

func TestInstallerDoesNothingWhenNothingIsMissing(t *testing.T) {
	installer := NewInstaller()
	if err := installer.Install(context.Background(), Wanted{}, "small", nil, nil); err != nil {
		t.Fatal(err)
	}
	if installer.Running() {
		t.Fatal("there was nothing to run")
	}
}

func TestInstallerRefusesToRunTwiceAtOnce(t *testing.T) {
	contain(t)
	installer := NewInstaller()
	installer.mu.Lock()
	installer.running = true
	installer.mu.Unlock()

	err := installer.Install(context.Background(), Wanted{Stages: []Stage{StageModel}}, "small", nil, nil)
	if err == nil {
		t.Fatal("two downloads of the same file would fight over the same partial")
	}
}

// Two of the three files is not a wake word: the runtime without the models
// loads nothing, and the models without the runtime load nothing either. What
// she gets from a partial set is a wake word that never fires, which feels
// exactly like a microphone that is switched off.
func TestWakeWantsEveryFileOrItWantsThemAll(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the runtime library is named differently elsewhere")
	}
	directory := contain(t)
	write(t, filepath.Join(directory, "whisper-cli.exe"), "binary")
	write(t, filepath.Join(directory, "ggml-small.bin"), "model")

	// Three, not four. The wake word itself is no longer downloaded -- it
	// ships inside the executable -- so what this stage owes her is the
	// runtime and the two stages every wake word runs through.
	files := []string{"onnxruntime.dll", "melspectrogram.onnx",
		"embedding_model.onnx"}

	for _, name := range files[:len(files)-1] {
		write(t, filepath.Join(directory, name), "present")
		if wanted := Missing(true, true, "small"); !wanted.Has(StageWake) {
			t.Fatalf("still incomplete after %s, but nothing was wanted", name)
		}
	}

	write(t, filepath.Join(directory, files[len(files)-1]), "present")
	if wanted := Missing(true, true, "small"); wanted.Has(StageWake) {
		t.Fatal("all three are here now")
	}
}

// A wake word file of somebody else's does not make the stage complete, and
// its absence does not make it incomplete. The stage is about the runtime and
// the two shared models now; the wake word arrives with the program.
func TestWakeIgnoresWakeWordFilesEntirely(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the runtime library is named differently elsewhere")
	}
	directory := contain(t)
	for _, name := range []string{"onnxruntime.dll", "melspectrogram.onnx",
		"embedding_model.onnx"} {
		write(t, filepath.Join(directory, name), "present")
	}

	if wanted := Missing(false, true, "small"); !wanted.Empty() {
		t.Fatalf("nothing is missing, got %v", wanted.Stages)
	}

	write(t, filepath.Join(directory, "alexa_v0.1.onnx"), "present")
	if wanted := Missing(false, true, "small"); !wanted.Empty() {
		t.Fatalf("another wake word changes nothing, got %v", wanted.Stages)
	}
}
