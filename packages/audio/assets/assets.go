// Package assets fetches the files recognition and the wake word need, so a
// fresh installation can hear.
//
// The large ones do not ship inside MikkiLens. The speech model alone is half a
// gigabyte, the CUDA build is another two thirds of one, and which build is
// right depends on the card in the machine -- so an installer carrying all of
// them would be a gigabyte of mostly-wrong binaries. They come down here
// instead, once, into data/models, where the engine already knows to look.
//
// The wake word files are the exception and go in the installer: eighteen
// megabytes, and the wake word is how she starts talking to MikkiLens without
// touching anything, so first run is the worst moment to need a network. An
// installed copy therefore never reaches stage 3 below; it is what somebody
// building from source sees.
//
// The ordering is the point. Each stage leaves the machine more useful than
// the last, so an interrupted download is never the difference between working
// and not:
//
//  1. the processor build      8 MB   -- something that can run at all
//  2. the speech model       488 MB   -- and now it can hear
//  3. the wake word           78 MB   -- and now it answers hands-free
//  4. the graphics build     670 MB   -- and now it answers in a fifth of a second
//
// Step 4 only happens on a machine with a graphics driver to run it, and it is
// last because it is an upgrade to something already working rather than a
// prerequisite for anything. chooseBuild in the stt package prefers it the
// moment it lands, with no restart and nothing to configure.
package assets

import (
	"os"
	"path/filepath"
	"runtime"

	"github.com/exzork/mikkilens/packages/audio/stt"
	"github.com/exzork/mikkilens/packages/core/paths"
)

// Releases are pinned rather than tracking the latest.
//
// A build that changed under her between one stream and the next, with no way
// to see what happened, is the opposite of what this application is for. These
// move when somebody bumps them and reads the release notes, not on their own.
const (
	whisperRelease = "v1.9.2"

	// onnxRuntime must match the ORT C API version that onnxruntime_go was
	// built against -- ORT_API_VERSION 29 in v1.35.0, which is runtime 1.29.
	// An older runtime loads and then refuses at startup with "Error setting
	// ORT API base", which reads as a broken wake word rather than as a
	// version that needs bumping alongside the Go dependency.
	onnxRuntime = RuntimeVersion

	wakeWordRelease  = "v0.5.1"
	whisperModelHost = "https://huggingface.co/ggerganov/whisper.cpp/resolve/main"
)

// Stage is one downloadable piece, named the way it is spoken about.
type Stage string

const (
	// StageEngine is the whisper.cpp build that runs on the processor.
	StageEngine Stage = "engine"
	// StageModel is the speech model itself.
	StageModel Stage = "model"
	// StageWake is the wake word runtime and its three ONNX files.
	StageWake Stage = "wake"
	// StageGPU is the CUDA build, which is an upgrade rather than a need.
	StageGPU Stage = "gpu"
)

// RuntimeVersion is the ONNX Runtime the wake word needs, named here so the
// wake package can say it when the library on disk answers a different one.
const RuntimeVersion = "1.29.0"

// Bytes is roughly how large each stage is, for what is said before it starts.
// Approximate on purpose: the exact number moves with every release, and it is
// used to say "about half a gigabyte", not to verify anything.
var Bytes = map[Stage]int64{
	StageEngine: 8_200_000,
	StageModel:  487_600_000,
	// The 1.29 runtime archive is 76 MB, plus a little over two for the two
	// shared models. Installed copies never reach this stage at all -- the
	// files ship inside the installer -- so it is what somebody building from
	// source hears.
	StageWake: 78_300_000,
	StageGPU:  670_600_000,
}

// modelsDir is where everything lands. A variable so tests can contain it.
var modelsDir = func() string { return paths.ModelsDir() }

// graphicsDriver reports whether the CUDA build is worth fetching. A variable
// for the same reason: whether this stage is wanted must not depend on which
// machine happens to be running the tests.
var graphicsDriver = stt.GraphicsDriverPresent

// gpuDir holds the graphics build. It is a directory of its own because the
// stt package prefers a GPU build found there over a processor one beside it,
// which is how both can sit on the machine at once.
func gpuDir() string { return filepath.Join(modelsDir(), "whisper") }

// Wanted describes what a machine still needs.
type Wanted struct {
	// Stages are what is missing, in the order they should be fetched.
	Stages []Stage
	// Bytes is roughly how much that adds up to.
	Bytes int64
}

// Empty reports whether there is nothing to do.
func (w Wanted) Empty() bool { return len(w.Stages) == 0 }

// Has reports whether one stage is in the list.
func (w Wanted) Has(stage Stage) bool {
	for _, candidate := range w.Stages {
		if candidate == stage {
			return true
		}
	}
	return false
}

// Missing works out what this machine still needs.
//
// speech and wakeWord are the two feature switches, so a person who has turned
// the wake word off is never made to wait for its files, and a person pointing
// recognition at a remote endpoint is never made to download a local one.
func Missing(speech, wakeWord bool, modelSize string) Wanted {
	wanted := Wanted{}

	if speech {
		if !engineInstalled() {
			wanted.Stages = append(wanted.Stages, StageEngine)
		}
		if !modelInstalled(modelSize) {
			wanted.Stages = append(wanted.Stages, StageModel)
		}
	}
	if wakeWord && !wakeInstalled() {
		wanted.Stages = append(wanted.Stages, StageWake)
	}
	// Only worth fetching where there is a driver to run it, and only once
	// the processor build is there to fall back on.
	if speech && runtime.GOOS == "windows" && !gpuInstalled() && graphicsDriver() {
		wanted.Stages = append(wanted.Stages, StageGPU)
	}

	for _, stage := range wanted.Stages {
		wanted.Bytes += Bytes[stage]
	}
	return wanted
}

// engineInstalled reports whether any whisper.cpp build is on the machine.
func engineInstalled() bool {
	for _, directory := range []string{modelsDir(), gpuDir()} {
		for _, name := range []string{"whisper-server.exe", "whisper-server",
			"whisper-cli.exe", "whisper-cli", "main.exe", "main"} {
			if exists(filepath.Join(directory, name)) {
				return true
			}
		}
	}
	return false
}

// modelInstalled reports whether the GGML model for one size is here.
//
// The same four names findModel in the stt package accepts, because a model
// that package would happily use is not one to download again.
func modelInstalled(size string) bool {
	if size == "" {
		size = "small"
	}
	for _, directory := range []string{modelsDir(), gpuDir()} {
		for _, suffix := range []string{".bin", ".en.bin", "-q5_1.bin", "-q8_0.bin"} {
			if exists(filepath.Join(directory, "ggml-"+size+suffix)) {
				return true
			}
		}
	}
	return false
}

// wakeInstalled reports whether the wake word has everything it needs.
//
// All four, not any: the runtime without the models loads nothing, and the
// models without the runtime load nothing either. Either way what she gets is
// a wake word that never fires, which feels exactly like a microphone that is
// switched off.
func wakeInstalled() bool {
	if !exists(filepath.Join(modelsDir(), runtimeLibrary())) {
		return false
	}
	for _, name := range shared {
		if !exists(filepath.Join(modelsDir(), name)) {
			return false
		}
	}
	return true
}

// shared are the two stages every wake word runs through, and the only two
// model files this package fetches for it.
//
// The wake word itself used to be here as a third download. It is not any
// more: MikkiLens ships its own, embedded in the executable, and the wake
// package writes it out. That removed the failure this stage was most likely
// to produce -- a runtime and two stages on the machine, and no wake word to
// run through them, which she experiences as a microphone that never answers.
var shared = []string{"melspectrogram.onnx", "embedding_model.onnx"}

// gpuInstalled reports whether a graphics build is already in place.
func gpuInstalled() bool {
	for _, name := range []string{"whisper-server.exe", "whisper-cli.exe"} {
		if exists(filepath.Join(gpuDir(), name)) {
			return true
		}
	}
	return false
}

func exists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Size() > 0
}

func runtimeLibrary() string {
	if runtime.GOOS == "windows" {
		return "onnxruntime.dll"
	}
	return "libonnxruntime.so"
}
