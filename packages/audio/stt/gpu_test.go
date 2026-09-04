package stt

import (
	"os"
	"path/filepath"
	"testing"
)

// Which build gets run is the difference between a command answered in a
// fifth of a second and one answered in three, so it is worth a test that
// does not need a graphics card to run.

func build(t *testing.T, directory, executable string, libraries ...string) string {
	t.Helper()
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range append(libraries, executable) {
		if err := os.WriteFile(filepath.Join(directory, name), []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(directory, executable)
}

func TestABuildWithCUDALibrariesBesideItCanUseTheCard(t *testing.T) {
	directory := t.TempDir()
	build(t, directory, "whisper-server.exe", "ggml-cuda.dll")

	if !buildUsesGPU(directory) {
		t.Error("a build shipping ggml-cuda.dll must be recognised as a GPU build")
	}
}

// Older whisper.cpp CUDA builds shipped no ggml-cuda.dll at all: the runtime
// came as versioned cublas and cudart files beside the executable.
func TestAnOlderCUDABuildIsRecognisedByItsRuntimeFiles(t *testing.T) {
	directory := t.TempDir()
	build(t, directory, "main.exe", "cublas64_12.dll", "cudart64_12.dll")

	if !buildUsesGPU(directory) {
		t.Error("a build shipping the CUDA runtime must be recognised as a GPU build")
	}
}

func TestAProcessorOnlyBuildIsNotMistakenForAGPUOne(t *testing.T) {
	directory := t.TempDir()
	build(t, directory, "whisper-server.exe", "ggml-cpu.dll", "whisper.dll")

	if buildUsesGPU(directory) {
		t.Error("a CPU build must not be taken for a GPU one; it would just be slow")
	}
}

// TestTheGPUBuildWinsEvenWhenItIsFoundSecond is the whole point of looking at
// every candidate rather than the first: data/models holds the CPU build she
// started with, and the GPU build lands in data/models/whisper beneath it.
func TestTheGPUBuildWinsEvenWhenItIsFoundSecond(t *testing.T) {
	if !gpuDriverPresent() {
		t.Skip("this machine has no graphics driver, so there is nothing to prefer")
	}
	root := t.TempDir()
	processor := build(t, root, "whisper-server.exe", "ggml-cpu.dll")
	card := build(t, filepath.Join(root, "whisper"), "whisper-server.exe", "ggml-cuda.dll")

	previous := binaryDirectories
	binaryDirectories = func() []string { return []string{root, filepath.Join(root, "whisper")} }
	t.Cleanup(func() { binaryDirectories = previous })

	chosen, err := chooseBuild("", "auto", []string{"whisper-server.exe"})
	if err != nil {
		t.Fatalf("chooseBuild: %v", err)
	}
	if chosen.binary != card {
		t.Errorf("chose %s, want the GPU build at %s", chosen.binary, card)
	}
	if !chosen.gpu {
		t.Error("the GPU build must be run with the card enabled")
	}
	if chosen.binary == processor {
		t.Error("the processor-only build was preferred")
	}
}

// "cpu" is a real answer, not a fallback: she may want the card left alone
// while it is busy with the game she is streaming.
func TestAskingForTheProcessorKeepsRecognitionOffTheCard(t *testing.T) {
	root := t.TempDir()
	build(t, root, "whisper-server.exe", "ggml-cuda.dll")

	previous := binaryDirectories
	binaryDirectories = func() []string { return []string{root} }
	t.Cleanup(func() { binaryDirectories = previous })

	chosen, err := chooseBuild("", "cpu", []string{"whisper-server.exe"})
	if err != nil {
		t.Fatalf("chooseBuild: %v", err)
	}
	if chosen.gpu {
		t.Error("device = cpu must not use the graphics card")
	}
}

// Asking for the card when there is no build for it still has to leave her
// with working recognition, and has to say why it is slow.
func TestAskingForTheCardWithNoGPUBuildFallsBackAndSaysSo(t *testing.T) {
	root := t.TempDir()
	build(t, root, "whisper-server.exe", "ggml-cpu.dll")

	previous := binaryDirectories
	binaryDirectories = func() []string { return []string{root} }
	t.Cleanup(func() { binaryDirectories = previous })

	chosen, err := chooseBuild("", "cuda", []string{"whisper-server.exe"})
	if err != nil {
		t.Fatalf("chooseBuild: %v", err)
	}
	if chosen.gpu {
		t.Error("there is no GPU build to use")
	}
	if chosen.note == "" {
		t.Error("a fallback nobody is told about is indistinguishable from recognition being slow")
	}
}

func TestBeamSizeFollowsWhereItRuns(t *testing.T) {
	if got := beamFor(0, true); got != 5 {
		t.Errorf("beamFor(auto, gpu) = %d, want a wide beam the card can afford", got)
	}
	if got := beamFor(0, false); got != 1 {
		t.Errorf("beamFor(auto, cpu) = %d, want a narrow beam so she is not left waiting", got)
	}
	if got := beamFor(3, true); got != 3 {
		t.Errorf("beamFor(3, gpu) = %d, want the configured beam honoured", got)
	}
}

func TestModelLabelReadsAsTheModelName(t *testing.T) {
	if got := modelLabel(filepath.Join("data", "models", "ggml-small.bin")); got != "small" {
		t.Errorf("modelLabel = %q, want %q", got, "small")
	}
}
