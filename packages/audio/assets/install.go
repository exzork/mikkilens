package assets

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/exzork/mikkilens/packages/core/paths"
)

// Error is an install failure worth reporting aloud.
type Error struct{ Reason string }

func (e *Error) Error() string { return e.Reason }

// Progress is how far along an install is.
//
// Stage names what is being fetched rather than which file, because the file
// names mean nothing said out loud: "the speech model" is an answer, and
// "ggml-small.bin" is a spelling test.
type Progress struct {
	Stage      Stage   `json:"stage"`
	Downloaded int64   `json:"downloaded"`
	Total      int64   `json:"total"`
	Percent    int     `json:"percent"`
	Speed      float64 `json:"bytes_per_second"`
	Done       bool    `json:"done"`
	Failed     string  `json:"failed,omitempty"`
}

// Installer fetches what Missing found, one stage at a time.
type Installer struct {
	mu      sync.Mutex
	running bool
	cancel  context.CancelFunc
	last    Progress
}

// NewInstaller makes an idle installer.
func NewInstaller() *Installer { return &Installer{} }

// Running reports whether a download is in progress.
func (i *Installer) Running() bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.running
}

// Progress is the most recent report, for the status page.
func (i *Installer) Progress() Progress {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.last
}

// Cancel stops a download in progress. What has come down is kept, so starting
// again resumes rather than beginning over.
func (i *Installer) Cancel() {
	i.mu.Lock()
	cancel := i.cancel
	i.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Install fetches every stage in wanted, in order, reporting as it goes.
//
// It returns immediately. onStage is called once as each stage begins and once
// when it finishes, and onProgress is called about twice a second in between;
// both run on a background goroutine. The engine turns the first into speech
// and the second into a number on the status page, because a download this
// size with no feedback is indistinguishable from one that has died, and a
// progress bar says nothing to somebody working by ear.
func (i *Installer) Install(ctx context.Context, wanted Wanted, modelSize string,
	onStage func(Progress), onProgress func(Progress)) error {

	if wanted.Empty() {
		return nil
	}

	i.mu.Lock()
	if i.running {
		i.mu.Unlock()
		return &Error{Reason: "a download is already running"}
	}
	ctx, cancel := context.WithCancel(ctx)
	i.running, i.cancel = true, cancel
	i.mu.Unlock()

	go func() {
		defer func() {
			cancel()
			i.mu.Lock()
			i.running, i.cancel = false, nil
			i.mu.Unlock()
		}()

		for _, stage := range wanted.Stages {
			i.report(Progress{Stage: stage}, onStage)

			if err := i.fetch(ctx, stage, modelSize, onProgress); err != nil {
				if ctx.Err() != nil {
					// Cancelling is a decision, not a fault. Saying it failed
					// would be a lie, and an alarming one mid-stream.
					i.report(Progress{Stage: stage, Done: true}, onStage)
					return
				}
				i.report(Progress{Stage: stage, Failed: err.Error()}, onStage)
				return
			}
			i.report(Progress{Stage: stage, Percent: 100, Done: true}, onStage)
		}
	}()
	return nil
}

func (i *Installer) report(progress Progress, notify func(Progress)) {
	i.mu.Lock()
	i.last = progress
	i.mu.Unlock()
	if notify != nil {
		notify(progress)
	}
}

// fetch does one stage.
func (i *Installer) fetch(ctx context.Context, stage Stage, modelSize string, onProgress func(Progress)) error {
	if _, err := paths.EnsureDataDir(); err != nil {
		return err
	}
	if err := os.MkdirAll(modelsDir(), 0o755); err != nil {
		return err
	}

	track := func(done, total int64, speed float64) {
		percent := 0
		if total > 0 {
			percent = int(float64(done) / float64(total) * 100)
		}
		i.report(Progress{
			Stage: stage, Downloaded: done, Total: total,
			Percent: percent, Speed: speed,
		}, onProgress)
	}

	switch stage {
	case StageEngine:
		return i.fetchArchive(ctx, whisperURL("whisper-bin-x64.zip"),
			"whisper-engine.zip", modelsDir(), wantedFromWhisper, track)

	case StageModel:
		if modelSize == "" {
			modelSize = "small"
		}
		name := "ggml-" + modelSize + ".bin"
		return download(ctx, whisperModelHost+"/"+name,
			filepath.Join(modelsDir(), name), Bytes[StageModel], track)

	case StageWake:
		return i.fetchWake(ctx, track)

	case StageGPU:
		return i.fetchArchive(ctx, whisperURL("whisper-cublas-12.4.0-bin-x64.zip"),
			"whisper-gpu.zip", gpuDir(), wantedFromWhisper, track)
	}
	return &Error{Reason: "unknown download stage " + string(stage)}
}

// fetchArchive downloads a zip, unpacks what is wanted, and removes it.
func (i *Installer) fetchArchive(ctx context.Context, url, temporaryName, directory string,
	want func(string) bool, track func(int64, int64, float64)) error {

	archive := filepath.Join(modelsDir(), temporaryName)
	if err := download(ctx, url, archive, 0, track); err != nil {
		return err
	}
	defer os.Remove(archive)
	return unpack(archive, directory, want)
}

// fetchWake gets the runtime and the three ONNX files the wake word needs.
//
// The runtime first. The models are small and the runtime is not, so fetching
// them the other way round would leave the common interruption -- a connection
// dropping partway -- with three files that cannot be loaded by anything.
func (i *Installer) fetchWake(ctx context.Context, track func(int64, int64, float64)) error {
	if !exists(filepath.Join(modelsDir(), runtimeLibrary())) {
		if runtime.GOOS != "windows" {
			return &Error{Reason: "the wake word runtime is only fetched " +
				"automatically on Windows; put libonnxruntime.so in data/models"}
		}
		url := fmt.Sprintf(
			"https://github.com/microsoft/onnxruntime/releases/download/v%s/onnxruntime-win-x64-%s.zip",
			onnxRuntime, onnxRuntime)
		if err := i.fetchArchive(ctx, url, "onnxruntime.zip",
			modelsDir(), wantedFromRuntime, track); err != nil {
			return err
		}
	}

	// The two shared stages. The wake word itself is not fetched: MikkiLens
	// ships its own, embedded in the executable and written out by the wake
	// package, so there is nothing here that can arrive late or not at all.
	for _, name := range []string{"melspectrogram.onnx", "embedding_model.onnx"} {
		target := filepath.Join(modelsDir(), name)
		if exists(target) {
			continue
		}
		url := fmt.Sprintf("https://github.com/dscripka/openWakeWord/releases/download/%s/%s",
			wakeWordRelease, name)
		if err := download(ctx, url, target, 0, track); err != nil {
			return err
		}
	}
	return nil
}

func whisperURL(asset string) string {
	return fmt.Sprintf("https://github.com/ggml-org/whisper.cpp/releases/download/%s/%s",
		whisperRelease, asset)
}
