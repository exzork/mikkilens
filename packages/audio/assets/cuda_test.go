package assets

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/exzork/mikkilens/packages/audio/onnx"
	"github.com/exzork/mikkilens/packages/core/paths"
)

// cuDNN arrived after the rest of the graphics runtime, so there are machines
// with everything but it. Those must be asked for cuDNN alone: asking again
// for the whole gigabyte they already have would be a download that achieves
// nothing and takes most of an hour on a home connection.

func containCUDA(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "windows" {
		t.Skip("the graphics runtime is only fetched on Windows")
	}
	previous := paths.Root()
	paths.SetRoot(t.TempDir())
	t.Cleanup(func() { paths.SetRoot(previous) })
	withGraphicsDriver(t)
	return onnx.CUDADir()
}

func place(t *testing.T, directory string, names ...string) {
	t.Helper()
	for _, name := range names {
		write(t, filepath.Join(directory, name), "x")
	}
}

var cudaRuntime = []string{
	"onnxruntime.dll", "onnxruntime_providers_cuda.dll", "onnxruntime_providers_shared.dll",
	"cublas64_12.dll", "cublasLt64_12.dll", "cudart64_12.dll",
}

func TestAMachineWithNothingWantsTheRuntimeThenCuDNN(t *testing.T) {
	containCUDA(t)

	wanted := MissingCUDA("omnivoice")
	if len(wanted.Stages) != 2 || wanted.Stages[0] != StageCUDA || wanted.Stages[1] != StageCuDNN {
		t.Fatalf("stages = %v, want the runtime first and cuDNN after it", wanted.Stages)
	}
	if wanted.Bytes != Bytes[StageCUDA]+Bytes[StageCuDNN] {
		t.Errorf("bytes = %d, want both stages counted", wanted.Bytes)
	}
}

func TestAMachineWithTheOldRuntimeWantsOnlyCuDNN(t *testing.T) {
	place(t, containCUDA(t), cudaRuntime...)

	wanted := MissingCUDA("omnivoice")
	if len(wanted.Stages) != 1 || wanted.Stages[0] != StageCuDNN {
		t.Fatalf("stages = %v, want cuDNN alone", wanted.Stages)
	}
}

// Half of cuDNN is not cuDNN. Its libraries load one another only when the
// first convolution runs, so a download that stopped partway would pass a
// check for the first file and then fail in the middle of a sentence.
func TestAnUnfinishedCuDNNIsAskedForAgain(t *testing.T) {
	directory := containCUDA(t)
	place(t, directory, cudaRuntime...)
	place(t, directory, "cudnn64_9.dll", "cudnn_graph64_9.dll")

	if wanted := MissingCUDA("omnivoice"); !wanted.Has(StageCuDNN) {
		t.Fatalf("stages = %v, want cuDNN fetched again", wanted.Stages)
	}
}

func TestEverythingInPlaceWantsNothing(t *testing.T) {
	directory := containCUDA(t)
	place(t, directory, cudaRuntime...)
	place(t, directory,
		"cudnn64_9.dll", "cudnn_cnn64_9.dll", "cudnn_engines_precompiled64_9.dll",
		"cudnn_engines_runtime_compiled64_9.dll", "cudnn_graph64_9.dll",
		"cudnn_heuristic64_9.dll", "cudnn_ops64_9.dll")

	if wanted := MissingCUDA("omnivoice"); !wanted.Empty() {
		t.Fatalf("stages = %v, want nothing", wanted.Stages)
	}
}

// The recurrent half of cuDNN is 271 MB that a graph of convolutions never
// loads. Everything else in the wheel is kept, including the engines that
// look optional and are not.
func TestCuDNNKeepsItsLibrariesButTheRecurrentOnes(t *testing.T) {
	for name, want := range map[string]bool{
		"cudnn64_9.dll":                     true,
		"cudnn_engines_precompiled64_9.dll": true,
		"cudnn_ops64_9.dll":                 true,
		"cudnn_adv64_9.dll":                 false,
		"cudnn.h":                           false,
		"METADATA":                          false,
	} {
		if got := wantedFromCuDNN(name); got != want {
			t.Errorf("wantedFromCuDNN(%q) = %v, want %v", name, got, want)
		}
	}
}
