package stt

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Choosing where recognition runs.
//
// whisper.cpp is not one build: the same executable name is shipped compiled
// for the processor alone, for CUDA, for Vulkan. Which one is in data/models
// decides whether a command takes a fifth of a second or three seconds, and
// three seconds of silence after speaking is long enough that she says it
// again -- and then the command arrives twice.
//
// So the build is chosen rather than assumed: if there is a GPU build on the
// machine and a driver to run it, that is the one used, and if there is not,
// recognition falls back to the processor and says so instead of failing.
// Nothing here rebuilds or downloads anything; it only looks.

// acceleratorLibraries are the files a GPU-capable whisper.cpp build ships
// alongside its executable. The first two are how recent builds split the
// backends out; the CUDA runtime pair catches the older monolithic ones.
var acceleratorLibraries = []string{
	"ggml-cuda.dll", "libggml-cuda.so",
	"ggml-vulkan.dll", "libggml-vulkan.so",
	"ggml-hip.dll", "libggml-hip.so",
}

// acceleratorPrefixes catch the versioned CUDA runtime files, whose names
// carry a version number that changes with every toolkit release.
var acceleratorPrefixes = []string{"cublas64_", "cudart64_", "libcublas.so", "libcudart.so"}

// buildUsesGPU reports whether the whisper.cpp build in one directory can use
// a graphics card at all.
func buildUsesGPU(directory string) bool {
	for _, name := range acceleratorLibraries {
		if info, err := os.Stat(filepath.Join(directory, name)); err == nil && !info.IsDir() {
			return true
		}
	}

	entries, err := os.ReadDir(directory)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		name := strings.ToLower(entry.Name())
		for _, prefix := range acceleratorPrefixes {
			if strings.HasPrefix(name, prefix) {
				return true
			}
		}
	}
	return false
}

// gpuDriverPresent reports whether this machine has a graphics driver a GPU
// build could actually use.
//
// The driver, not the card: a build that finds no driver falls back to the
// processor on its own, but it does so silently and seconds later, which is
// exactly the failure this whole file exists to make visible.
func gpuDriverPresent() bool {
	if _, err := exec.LookPath("nvidia-smi"); err == nil {
		return true
	}
	for _, candidate := range driverLibraries() {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return true
		}
	}
	return false
}

func driverLibraries() []string {
	if runtime.GOOS == "windows" {
		root := os.Getenv("SystemRoot")
		if root == "" {
			root = `C:\Windows`
		}
		return []string{
			filepath.Join(root, "System32", "nvcuda.dll"),
			filepath.Join(root, "System32", "vulkan-1.dll"),
		}
	}
	return []string{
		"/usr/lib/x86_64-linux-gnu/libcuda.so.1",
		"/usr/lib64/libcuda.so.1",
	}
}

// recognitionBuild is one whisper.cpp executable and how it will be run.
type recognitionBuild struct {
	binary string
	gpu    bool

	// note is why it is not on the GPU when it could have been asked to be.
	// It is logged and shown in the status page rather than swallowed: "why is
	// recognition suddenly slow" is otherwise unanswerable from the outside.
	note string
}

// where names the accelerator in the words the status page and the spoken
// self test use.
func (b recognitionBuild) where() string {
	if b.gpu {
		return "the graphics card"
	}
	return "the processor"
}

// chooseBuild picks which whisper.cpp executable to run, and whether to let it
// use the graphics card.
//
// The configured device is honoured where it can be: "cpu" always stays on the
// processor, and "cuda" or "gpu" asks for the card but still falls back rather
// than leaving her with no recognition at all. "auto" -- the default -- uses
// the card whenever there is a build and a driver for it.
func chooseBuild(configured string, device string, names []string) (recognitionBuild, error) {
	wanted := strings.ToLower(strings.TrimSpace(device))

	candidates, err := namedBinaries(configured, names)
	if err != nil {
		return recognitionBuild{}, err
	}
	if wanted == "cpu" {
		return recognitionBuild{binary: candidates[0], gpu: false}, nil
	}

	for _, candidate := range candidates {
		if !buildUsesGPU(filepath.Dir(candidate)) {
			continue
		}
		if !gpuDriverPresent() {
			return recognitionBuild{binary: candidates[0], note: "there is a GPU " +
				"build of whisper.cpp but no graphics driver to run it, so " +
				"recognition is on the processor"}, nil
		}
		return recognitionBuild{binary: candidate, gpu: true}, nil
	}

	note := ""
	if wanted != "" && wanted != "auto" {
		// She asked for the card by name, so the fallback is not something to
		// discover later from the timing.
		note = "no GPU build of whisper.cpp was found in data/models, so " +
			"recognition is on the processor"
	}
	return recognitionBuild{binary: candidates[0], note: note}, nil
}

// GraphicsDriverPresent reports whether this machine has a graphics driver a
// GPU build could use.
//
// Exported for the assets package, which uses it to decide whether the CUDA
// build of whisper.cpp is worth fetching. Downloading two thirds of a gigabyte
// onto a machine with no driver to run it would be a long wait for nothing.
func GraphicsDriverPresent() bool { return gpuDriverPresent() }
