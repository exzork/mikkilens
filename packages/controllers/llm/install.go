package llm

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/exzork/mikkilens/packages/core/paths"
)

// Getting the model onto the machine without leaving the application.
//
// The runtime is small and comes down on its own the first time it is needed.
// The model is gigabytes, so it is never fetched without being asked for: that
// is her decision to make, on her connection, and it is the one thing here
// worth a deliberate yes.
//
// Everything reports progress, because a download this size with no feedback
// is indistinguishable from one that has died.

// Model is a language model MikkiLens knows how to fetch.
type Model struct {
	// Name is what she picks in the settings.
	Name string
	// File is the name on disk, which is also how a downloaded model is
	// recognised on the next start.
	File string
	// URL is where it comes from.
	URL string
	// Bytes is the download size, so she is told before agreeing to it.
	Bytes int64

	// ProjectorFile and ProjectorURL are the multimodal projector, which is
	// what lets a model look at a screenshot. Empty means text only.
	//
	// This is a separate file in llama.cpp, and a model GGUF without one
	// cannot see anything no matter what the original model could do.
	ProjectorFile  string
	ProjectorURL   string
	ProjectorBytes int64

	// Summary is one line about the trade it makes.
	Summary string
}

// Vision reports whether this model can describe a screenshot.
func (m Model) Vision() bool { return m.ProjectorURL != "" }

// TotalBytes is everything that has to come down.
func (m Model) TotalBytes() int64 { return m.Bytes + m.ProjectorBytes }

// Models are the choices offered.
//
// Gemma 4, in the E variants Google builds for running on the machine in front
// of you rather than in a data centre. Both are quantisation-aware training
// builds published by Google itself, which hold up better at four bits than
// the same size quantised after the fact.
//
// Both can see. That matters more than it looks: it means one download gives
// her a command matcher and a screen describer at once, and the screen
// describer -- the feature that exists purely because she cannot see -- stops
// needing an account, a payment card and an API key pasted into a settings
// page by somebody else.
//
// Gemma 3n was the obvious choice here and turned out not to be: its GGUF
// conversions are text only, with no projector anywhere, so the vision and
// audio encoders the original model has are simply absent.
var Models = []Model{
	{
		Name:           "gemma-4-e2b",
		File:           "gemma-4-E2B_q4_0-it.gguf",
		URL:            "https://huggingface.co/google/gemma-4-E2B-it-qat-q4_0-gguf/resolve/main/gemma-4-E2B_q4_0-it.gguf",
		Bytes:          3349516256,
		ProjectorFile:  "gemma-4-E2B-it-mmproj.gguf",
		ProjectorURL:   "https://huggingface.co/google/gemma-4-E2B-it-qat-q4_0-gguf/resolve/main/gemma-4-E2B-it-mmproj.gguf",
		ProjectorBytes: 986833664,
		Summary:        "smaller and quicker; understands commands and sees your screen",
	},
	{
		Name:           "gemma-4-e4b",
		File:           "gemma-4-E4B_q4_0-it.gguf",
		URL:            "https://huggingface.co/google/gemma-4-E4B-it-qat-q4_0-gguf/resolve/main/gemma-4-E4B_q4_0-it.gguf",
		Bytes:          5154941280,
		ProjectorFile:  "gemma-4-E4B-it-mmproj.gguf",
		ProjectorURL:   "https://huggingface.co/google/gemma-4-E4B-it-qat-q4_0-gguf/resolve/main/gemma-4-E4B-it-mmproj.gguf",
		ProjectorBytes: 991552256,
		Summary:        "understands more, and describes the screen in more detail; slower",
	},
}

// ModelByName finds a known model.
func ModelByName(name string) (Model, bool) {
	for _, model := range Models {
		if strings.EqualFold(model.Name, name) {
			return model, true
		}
	}
	return Model{}, false
}

// runtimeRelease is the llama.cpp build to fetch when none is installed.
//
// Pinned rather than tracking the latest: a build that changed under her
// between one stream and the next, with no way to see what happened, is the
// opposite of what this application is for. The plain CPU build is the one
// that works everywhere; a CUDA or Vulkan build can be dropped into
// data/models/llama by hand and will be preferred automatically.
const runtimeRelease = "b10679"

func runtimeURL() string {
	architecture := "x64"
	if runtime.GOARCH == "arm64" {
		architecture = "arm64"
	}
	return fmt.Sprintf(
		"https://github.com/ggml-org/llama.cpp/releases/download/%s/llama-%s-bin-win-cpu-%s.zip",
		runtimeRelease, runtimeRelease, architecture)
}

// Progress describes how far along an install is.
type Progress struct {
	Stage      string  `json:"stage"` // "runtime" | "model" | "done" | "error"
	Downloaded int64   `json:"downloaded"`
	Total      int64   `json:"total"`
	Percent    int     `json:"percent"`
	Detail     string  `json:"detail"`
	Speed      float64 `json:"bytes_per_second"`
}

// Installer downloads the runtime and a model, one at a time.
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

// Progress is the most recent report.
func (i *Installer) Progress() Progress {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.last
}

// Cancel stops a download in progress. What has been fetched is kept, so
// starting again resumes rather than beginning over.
func (i *Installer) Cancel() {
	i.mu.Lock()
	cancel := i.cancel
	i.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Install fetches the runtime if it is missing and then the model, reporting
// progress as it goes. It returns immediately; onProgress is called from a
// background goroutine and again at the end with "done" or "error".
func (i *Installer) Install(model Model, onProgress func(Progress)) error {
	i.mu.Lock()
	if i.running {
		i.mu.Unlock()
		return &Error{Reason: "a download is already running"}
	}
	ctx, cancel := context.WithCancel(context.Background())
	i.running, i.cancel = true, cancel
	i.mu.Unlock()

	report := func(progress Progress) {
		i.mu.Lock()
		i.last = progress
		i.mu.Unlock()
		if onProgress != nil {
			onProgress(progress)
		}
	}

	go func() {
		defer func() {
			cancel()
			i.mu.Lock()
			i.running, i.cancel = false, nil
			i.mu.Unlock()
		}()

		if err := i.run(ctx, model, report); err != nil {
			report(Progress{Stage: "error", Detail: err.Error()})
			return
		}
		report(Progress{Stage: "done", Percent: 100, Detail: model.File})
	}()
	return nil
}

func (i *Installer) run(ctx context.Context, model Model, report func(Progress)) error {
	if _, err := paths.EnsureDataDir(); err != nil {
		return err
	}
	directory := filepath.Join(paths.ModelsDir(), "llama")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return err
	}

	// The runtime first: a model with nothing to run it is 3 gigabytes of no
	// use at all, and finding that out after the long download would be a
	// miserable way to learn it.
	if _, err := FindServerBinary(); err != nil {
		archive := filepath.Join(directory, "runtime.zip")
		if err := download(ctx, runtimeURL(), archive, 0, func(done, total int64, speed float64) {
			report(progressOf("runtime", done, total, speed,
				"the model runtime"))
		}); err != nil {
			return &Error{Reason: "could not download the model runtime: " + err.Error()}
		}
		if err := unzip(archive, directory); err != nil {
			return &Error{Reason: "could not unpack the model runtime: " + err.Error()}
		}
		_ = os.Remove(archive)
		if _, err := FindServerBinary(); err != nil {
			return &Error{Reason: "the model runtime unpacked but no server was found in it"}
		}
	}

	target := filepath.Join(paths.ModelsDir(), model.File)
	if info, err := os.Stat(target); err != nil || info.Size() == 0 {
		if err := download(ctx, model.URL, target, model.Bytes,
			func(done, total int64, speed float64) {
				report(progressOf("model", done, total, speed, model.Name))
			}); err != nil {
			return err
		}
	}

	// The projector last. Without it the model still answers commands, so an
	// interrupted download leaves something useful rather than nothing.
	if !model.Vision() {
		return nil
	}
	projector := filepath.Join(paths.ModelsDir(), model.ProjectorFile)
	if info, err := os.Stat(projector); err == nil && info.Size() > 0 {
		return nil
	}
	return download(ctx, model.ProjectorURL, projector, model.ProjectorBytes,
		func(done, total int64, speed float64) {
			report(progressOf("projector", done, total, speed, model.Name))
		})
}

// ProjectorFor finds the projector belonging to a model file, if it is here.
func ProjectorFor(modelPath string) string {
	name := filepath.Base(modelPath)
	for _, model := range Models {
		if model.File != name || !model.Vision() {
			continue
		}
		candidate := filepath.Join(filepath.Dir(modelPath), model.ProjectorFile)
		if info, err := os.Stat(candidate); err == nil && info.Size() > 0 {
			return candidate
		}
	}
	return ""
}

func progressOf(stage string, done, total int64, speed float64, detail string) Progress {
	percent := 0
	if total > 0 {
		percent = int(float64(done) / float64(total) * 100)
	}
	return Progress{
		Stage: stage, Downloaded: done, Total: total,
		Percent: percent, Detail: detail, Speed: speed,
	}
}

// download fetches one file, resuming a partial one rather than starting over.
//
// Three gigabytes over a home connection is long enough that something will
// interrupt it -- a dropped link, a closed laptop, a stream that needed the
// bandwidth. Starting again from nothing each time would make it effectively
// impossible on a slow connection.
func download(ctx context.Context, url, target string, expected int64, onProgress func(done, total int64, speed float64)) error {
	partial := target + ".part"

	var existing int64
	if info, err := os.Stat(partial); err == nil {
		existing = info.Size()
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if existing > 0 {
		request.Header.Set("Range", fmt.Sprintf("bytes=%d-", existing))
	}

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	switch response.StatusCode {
	case http.StatusOK:
		existing = 0 // the server ignored the range, so start over
	case http.StatusPartialContent:
		slog.Info("resuming a download", "have", existing, "url", url)
	default:
		return fmt.Errorf("the download failed: %s", response.Status)
	}

	total := expected
	if total <= 0 {
		total = existing + response.ContentLength
	}

	flags := os.O_CREATE | os.O_WRONLY
	if existing > 0 {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	file, err := os.OpenFile(partial, flags, 0o644)
	if err != nil {
		return err
	}

	written := existing
	started := time.Now()
	buffer := make([]byte, 1<<20)
	lastReport := time.Now()

	for {
		read, readErr := response.Body.Read(buffer)
		if read > 0 {
			if _, err := file.Write(buffer[:read]); err != nil {
				file.Close()
				return err
			}
			written += int64(read)

			// Reported about twice a second: often enough to sound alive,
			// rarely enough not to flood the settings page with updates.
			if time.Since(lastReport) > 500*time.Millisecond {
				lastReport = time.Now()
				elapsed := time.Since(started).Seconds()
				speed := 0.0
				if elapsed > 0 {
					speed = float64(written-existing) / elapsed
				}
				onProgress(written, total, speed)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			file.Close()
			return readErr
		}
		if ctx.Err() != nil {
			file.Close()
			return ctx.Err()
		}
	}

	if err := file.Close(); err != nil {
		return err
	}
	// Renamed only once it is whole, so an interrupted download is never
	// mistaken for a usable model on the next start.
	return os.Rename(partial, target)
}

// unzip extracts an archive flatly: the release puts its binaries in the root
// or one level down, and what matters is that llama-server ends up somewhere
// the search path looks.
func unzip(archive, directory string) error {
	reader, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer reader.Close()

	for _, entry := range reader.File {
		if entry.FileInfo().IsDir() {
			continue
		}
		name := filepath.Base(entry.Name)
		if name == "" || strings.HasPrefix(name, ".") {
			continue
		}

		source, err := entry.Open()
		if err != nil {
			return err
		}
		target, err := os.OpenFile(filepath.Join(directory, name),
			os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
		if err != nil {
			source.Close()
			return err
		}
		_, err = io.Copy(target, source)
		source.Close()
		target.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// InstalledModel reports which model is on disk, if any.
func InstalledModel() string {
	path, err := FindModelFile()
	if err != nil {
		return ""
	}
	return filepath.Base(path)
}

// RuntimeInstalled reports whether the server binary is present.
func RuntimeInstalled() bool {
	_, err := FindServerBinary()
	return err == nil
}

// MarshalModels is the catalogue for the settings page.
func MarshalModels() []map[string]any {
	listed := make([]map[string]any, 0, len(Models))
	for _, model := range Models {
		listed = append(listed, map[string]any{
			"name":    model.Name,
			"file":    model.File,
			"bytes":   model.TotalBytes(),
			"vision":  model.Vision(),
			"summary": model.Summary,
		})
	}
	return listed
}
