package omnivoice

import (
	"context"
	"os"
	"testing"
	"time"

	ort "github.com/yalue/onnxruntime_go"
)

// How fast OmniVoice is on the graphics card, measured rather than assumed.
//
// This is the number the whole engine hangs on. On a processor it is about
// twenty-five seconds of work per second of speech, which is a voice for
// recordings rather than for a live stream; the question this test answers is
// whether a card changes that by enough to matter.
//
// It needs a build of ONNX Runtime with the CUDA provider in it, which is not
// the build MikkiLens installs -- so it is pointed at one by hand:
//
//	MIKKILENS_LIVE=1 MIKKILENS_ORT_GPU=C:\path\to\onnxruntime-gpu\ go test ...
//
// The directory must hold onnxruntime.dll, its provider DLLs, and the CUDA
// runtime DLLs they link against.
func TestSpeedOnTheGraphicsCardLive(t *testing.T) {
	liveOrSkip(t)

	runtimeDir := os.Getenv("MIKKILENS_ORT_GPU")
	if runtimeDir == "" {
		t.Skip("set MIKKILENS_ORT_GPU to a runtime with the CUDA provider")
	}

	// Initialised here rather than through onnx.Start, which finds the
	// processor-only runtime beside the models. Start is idempotent and checks
	// whether the environment is already up, so claiming it first with a
	// different library is enough to redirect everything in the process --
	// without touching the installed runtime the rest of MikkiLens uses.
	if !ort.IsInitialized() {
		ort.SetSharedLibraryPath(runtimeDir + string(os.PathSeparator) + "onnxruntime.dll")
		if err := ort.InitializeEnvironment(); err != nil {
			t.Fatalf("starting the graphics runtime: %v", err)
		}
	}

	engine, err := Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer engine.Close()

	if !engine.Accelerated() {
		t.Fatal("the CUDA provider was refused; this runtime has no usable card")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	const text = "Halo, ini contoh suara MikkiLens. Kamu sudah live."

	// Twice. The first sentence pays for the card warming up -- allocating its
	// arena, compiling kernels -- and quoting that as the speed would be
	// quoting the one number nobody experiences after the first sentence of a
	// stream.
	for attempt := 1; attempt <= 2; attempt++ {
		started := time.Now()
		samples, err := engine.Speak(ctx, text, Options{Language: "id"})
		took := time.Since(started)
		if err != nil {
			t.Fatalf("Speak: %v", err)
		}
		seconds := float64(len(samples)) / SampleRate
		t.Logf("pass %d: %.2fs of audio in %s (%.2fx real time, %d steps)",
			attempt, seconds, took.Round(time.Millisecond),
			took.Seconds()/seconds, DefaultSteps)
	}
}
