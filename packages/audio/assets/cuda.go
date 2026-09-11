package assets

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/exzork/mikkilens/packages/audio/onnx"
)

// StageCUDA is a build of ONNX Runtime that can use the graphics card, and the
// NVIDIA libraries it loads. About a gigabyte.
//
// It exists for exactly one model. OmniVoice on a processor is roughly
// twenty-five seconds of work per second of speech -- a sentence takes half a
// minute, which is not a voice anybody can stream with. On a card it is faster
// than real time. Nothing else in MikkiLens is close to that line: the wake
// word and Supertonic are small enough that moving them to a card would cost
// more in copying than it saved, and they are left on the processor whether
// this is installed or not.
//
// So it is fetched only when OmniVoice is the chosen voice and only when there
// is an NVIDIA card to use it. See MissingCUDA.
const StageCUDA Stage = "cuda"

// The pieces, pinned. A runtime that changed between one stream and the next
// with no way to see what happened is the opposite of what this application is
// for -- and here it would not even fail loudly: a mismatched CUDA library
// falls back to the processor, and the only symptom is that every sentence
// suddenly takes half a minute.
//
// cuDNN is deliberately absent, and is the reason the audio decoder stays on
// the processor. It is another gigabyte of libraries, and the only thing in
// this application that would load it is that one graph's convolutions, which
// run once per sentence against the language model's thirty-two passes. See
// the note in omnivoice.Open.
var cudaFiles = []struct {
	url   string
	as    string
	bytes int64
	want  func(string) bool
}{
	{
		url: "https://github.com/microsoft/onnxruntime/releases/download/v" +
			onnx.RuntimeVersion + "/onnxruntime-win-x64-gpu_cuda12-" +
			onnx.RuntimeVersion + ".zip",
		as:    "onnxruntime-cuda.zip",
		bytes: 362_974_227,
		want:  wantedFromCUDARuntime,
	},
	{
		url: "https://files.pythonhosted.org/packages/20/e2/" +
			"fc9a0e985249d873150276d5afb02e39a66817fedbf1a385724393e505ed/" +
			"nvidia_cublas_cu12-12.9.2.10-py3-none-win_amd64.whl",
		as:    "cublas.zip",
		bytes: 553_162_896,
		want:  wantedFromCUDALibrary,
	},
	{
		url: "https://files.pythonhosted.org/packages/59/df/" +
			"e7c3a360be4f7b93cee39271b792669baeb3846c58a4df6dfcf187a7ffab/" +
			"nvidia_cuda_runtime_cu12-12.9.79-py3-none-win_amd64.whl",
		as:    "cudart.zip",
		bytes: 3_591_604,
		want:  wantedFromCUDALibrary,
	},
	{
		// Without this the runtime still works and takes the better part of a
		// minute to open its sessions rather than ten seconds, because cuBLAS
		// falls back to compiling kernels the slow way. Seventy-five megabytes
		// against forty seconds on the first sentence of every stream.
		url: "https://files.pythonhosted.org/packages/52/de/" +
			"823919be3b9d0ccbf1f784035423c5f18f4267fb0123558d58b813c6ec86/" +
			"nvidia_cuda_nvrtc_cu12-12.9.86-py3-none-win_amd64.whl",
		as:    "nvrtc.zip",
		bytes: 76_408_187,
		want:  wantedFromCUDALibrary,
	},
}

// wantedFromCUDARuntime keeps the runtime and its two provider libraries. The
// TensorRT provider is left behind: it is another path to the same card that
// needs a separate SDK this application does not ship.
func wantedFromCUDARuntime(name string) bool {
	switch strings.ToLower(name) {
	case "onnxruntime.dll",
		"onnxruntime_providers_cuda.dll",
		"onnxruntime_providers_shared.dll":
		return true
	}
	return false
}

// wantedFromCUDALibrary keeps the DLLs out of a Python wheel and leaves the
// metadata, the headers and the import libraries behind. A wheel is a zip, so
// no special handling is needed to read one -- only the knowledge that most of
// what is in it is for a compiler rather than for us.
func wantedFromCUDALibrary(name string) bool {
	return strings.HasSuffix(strings.ToLower(name), ".dll")
}

// MissingCUDA is what the graphics path still needs.
//
// Three conditions, all of them necessary. OmniVoice has to be the chosen
// voice, because nothing else would use this. There has to be an NVIDIA driver,
// because without one this is a gigabyte that will never be loaded. And it has
// to not already be here.
func MissingCUDA(engine string) Wanted {
	if engine != "omnivoice" || runtime.GOOS != "windows" {
		return Wanted{}
	}
	if !graphicsDriver() {
		return Wanted{}
	}
	if cudaInstalled() {
		return Wanted{}
	}
	return Wanted{Stages: []Stage{StageCUDA}, Bytes: Bytes[StageCUDA]}
}

// cudaInstalled reports whether the graphics runtime is already in place.
//
// All of it, not some. A runtime with no cuBLAS beside it loads, refuses the
// card, and falls back to the processor -- which is not an error anywhere, just
// a voice that is sixty times slower than it should be.
func cudaInstalled() bool {
	for _, name := range []string{
		"onnxruntime.dll",
		"onnxruntime_providers_cuda.dll",
		"onnxruntime_providers_shared.dll",
		"cublas64_12.dll",
		"cublasLt64_12.dll",
		"cudart64_12.dll",
	} {
		if !exists(filepath.Join(onnx.CUDADir(), name)) {
			return false
		}
	}
	return true
}

// WithCUDA puts the graphics runtime in front of OmniVoice's own models.
//
// In front, because it is the half of the pair that decides whether the other
// half is usable. Interrupted the other way round, somebody is left with two
// gigabytes of a voice that works and takes half a minute a sentence, which is
// a much harder thing to diagnose than a download that plainly did not finish.
func WithCUDA(wanted, cuda Wanted) Wanted {
	if cuda.Empty() {
		return wanted
	}
	return Wanted{
		Stages: append(append([]Stage{}, cuda.Stages...), wanted.Stages...),
		Bytes:  wanted.Bytes + cuda.Bytes,
	}
}

// fetchCUDA downloads the runtime and the libraries it loads.
func (i *Installer) fetchCUDA(ctx context.Context, track func(int64, int64, float64)) error {
	if runtime.GOOS != "windows" {
		return &Error{Reason: "the graphics runtime is only fetched automatically on Windows"}
	}
	for _, file := range cudaFiles {
		if err := i.fetchArchive(ctx, file.url, file.as,
			onnx.CUDADir(), file.want, track); err != nil {
			return fmt.Errorf("fetching %s: %w", file.as, err)
		}
	}
	return nil
}

// cudaBytes is what StageCUDA adds up to, summed from the parts rather than
// written down beside them. Two numbers that have to agree eventually do not.
func cudaBytes() int64 {
	var total int64
	for _, file := range cudaFiles {
		total += file.bytes
	}
	return total
}
