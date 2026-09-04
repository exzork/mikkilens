// Command score reports what the wake-word detector makes of a WAV file.
//
// It is the honest end of the training tool. Everything before it -- the
// synthesis, the features, the classifier -- runs in Python against its own
// reimplementation of the pipeline, and a reimplementation that has drifted
// produces a model that trains beautifully and never fires. This runs the
// engine's own detector, over the engine's own ONNX runtime, on the model file
// as installed, and reports whether it actually fired.
//
//	go run ./tools/wakeword/score -model mikkilens clips/*.wav
//
// It is also the thing to reach for when a wake word has stopped working: feed
// it a recording and it will say what the detector scored, which is the one
// number nothing else in MikkiLens shows you.
package main

import (
	"encoding/csv"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/exzork/mikkilens/packages/audio/tts"
	"github.com/exzork/mikkilens/packages/audio/wake"
)

func main() {
	model := flag.String("model", "mikkilens", "wake word model in data/models")
	threshold := flag.Float64("threshold", 0.5, "score at which it counts as fired")
	// The engine's two-second cooldown is wall-clock, and a file is pushed
	// through in a fraction of the time it would take to say. Left at its
	// normal value it suppresses every detection after the first and the run
	// reports that nothing fired.
	cooldown := flag.Float64("cooldown", 0.001, "seconds before it may fire again")
	flag.Parse()

	files := flag.Args()
	if len(files) == 0 {
		fmt.Fprintln(os.Stderr, "usage: score [-model NAME] [-threshold N] FILE...")
		os.Exit(2)
	}

	fired := false
	detector := wake.New(wake.Options{
		Model:      *model,
		Threshold:  *threshold,
		CooldownS:  *cooldown,
		OnDetected: func(string, float64) { fired = true },
	})
	if err := detector.Load(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer detector.Close()

	out := csv.NewWriter(os.Stdout)
	out.Comma = '\t'
	defer out.Flush()
	_ = out.Write([]string{"file", "peak", "fired"})

	hits := 0
	for _, file := range files {
		peak, heard, err := play(detector, file, &fired)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", filepath.Base(file), err)
			continue
		}
		if heard {
			hits++
		}
		_ = out.Write([]string{
			filepath.Base(file),
			fmt.Sprintf("%.4f", peak),
			fmt.Sprintf("%t", heard),
		})
	}
	out.Flush()
	fmt.Fprintf(os.Stderr, "%d of %d fired at %.2f\n", hits, len(files), *threshold)
}

// play feeds one file through the detector and reports its highest score.
//
// The pause between chunks is not politeness. The detector scores on its own
// goroutine and drops a chunk rather than stall the microphone, which is right
// when the microphone is real and wrong when the audio is a file being pushed
// through as fast as the disk allows.
func play(detector *wake.Detector, path string, fired *bool) (float64, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false, err
	}
	audio, err := tts.Decode(data)
	if err != nil {
		return 0, false, err
	}
	if audio.SampleRate != 16000 {
		return 0, false, fmt.Errorf("%d Hz, want 16000", audio.SampleRate)
	}

	samples := audio.Samples
	if audio.Channels > 1 {
		mono := make([]float32, audio.Frames())
		for frame := range mono {
			var sum float32
			for channel := 0; channel < audio.Channels; channel++ {
				sum += samples[frame*audio.Channels+channel]
			}
			mono[frame] = sum / float32(audio.Channels)
		}
		samples = mono
	}

	*fired = false
	detector.Resume()

	peak := 0.0
	for start := 0; start+wake.ChunkSamples <= len(samples); start += wake.ChunkSamples {
		detector.Feed(samples[start : start+wake.ChunkSamples])
		time.Sleep(4 * time.Millisecond)
		if score := detector.LastScore(); score > peak {
			peak = score
		}
	}
	// One last look, in case the final chunk was still being scored.
	time.Sleep(20 * time.Millisecond)
	if score := detector.LastScore(); score > peak {
		peak = score
	}
	return peak, *fired, nil
}
