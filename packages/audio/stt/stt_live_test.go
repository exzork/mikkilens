package stt_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/exzork/mikkilens/packages/audio/stt"
	"github.com/exzork/mikkilens/packages/audio/tts"
	"github.com/exzork/mikkilens/packages/core/config"
	"github.com/exzork/mikkilens/packages/core/intent"
	"github.com/exzork/mikkilens/packages/core/paths"
)

// The whole path, end to end: MikkiLens says a command in its own voice,
// hears it back, and routes it to the right handler.
//
// This is the loop that matters and the one hardest to be sure about by
// reading code. It needs the speech model installed and the network for
// synthesis, so it is skipped unless MIKKILENS_LIVE=1.

func requireEverything(t *testing.T) config.Config {
	t.Helper()
	if os.Getenv("MIKKILENS_LIVE") != "1" {
		t.Skip("set MIKKILENS_LIVE=1 to exercise synthesis and recognition together")
	}

	root := repoRoot(t)
	paths.SetRoot(root)

	models := filepath.Join(root, "data", "models")
	entries, err := os.ReadDir(models)
	if err != nil {
		t.Skipf("no speech model installed in %s", models)
	}
	hasModel, hasBinary := false, false
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, "ggml-") && strings.HasSuffix(name, ".bin") {
			hasModel = true
		}
		if name == "whisper-cli.exe" || name == "main.exe" || name == "whisper-cli" {
			hasBinary = true
		}
	}
	if !hasModel || !hasBinary {
		t.Skip("whisper.cpp and a GGML model are not installed in data/models")
	}

	settings, err := config.Load("")
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	return settings
}

func repoRoot(t *testing.T) string {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(directory, "go.mod")); err == nil {
			return directory
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			t.Fatal("could not find the repository root")
		}
		directory = parent
	}
}

// speak synthesizes a phrase and hands it back as the mono 16 kHz the
// recognizer wants, which is the same shape the microphone produces.
func speak(t *testing.T, phrase string) []float32 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	audio, err := tts.Synthesize(ctx, phrase, tts.Options{
		Voice: "id-ID-GadisNeural", Rate: "+0%", Volume: "+0%",
	})
	if err != nil {
		t.Skipf("could not synthesize (offline?): %v", err)
	}
	return toMono16k(audio)
}

// toMono16k averages the channels and resamples. Linear interpolation is
// enough here: the point is to check recognition, not to make a nice recording.
func toMono16k(audio tts.Audio) []float32 {
	channels := max(1, audio.Channels)
	frames := len(audio.Samples) / channels

	mono := make([]float32, frames)
	for frame := 0; frame < frames; frame++ {
		total := float32(0)
		for channel := 0; channel < channels; channel++ {
			total += audio.Samples[frame*channels+channel]
		}
		mono[frame] = total / float32(channels)
	}
	if audio.SampleRate == stt.SampleRate || audio.SampleRate == 0 {
		return mono
	}

	out := make([]float32, int(float64(frames)*float64(stt.SampleRate)/float64(audio.SampleRate)))
	for index := range out {
		at := float64(index) * float64(audio.SampleRate) / float64(stt.SampleRate)
		low := int(at)
		high := min(low+1, frames-1)
		out[index] = mono[low] + (mono[high]-mono[low])*float32(at-float64(low))
	}
	return out
}

// TestHearsItsOwnVoice is the end-to-end check: synthesize a real command,
// recognize it, and route it.
func TestHearsItsOwnVoice(t *testing.T) {
	settings := requireEverything(t)

	transcriber := stt.New(settings.STT, "id")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	if err := transcriber.Load(ctx); err != nil {
		t.Fatalf("Load: %v", err)
	}
	t.Logf("backend: %s", transcriber.Describe())

	commands, err := intent.SetFromFile(paths.CommandsFile("id"))
	if err != nil {
		t.Fatalf("commands: %v", err)
	}

	spoken := []struct{ phrase, want string }{
		{"matikan mikrofon", "mute_mic"},
		{"berapa penontonnya", "viewer_count"},
		{"ganti ke just chatting", "switch_scene"},
	}

	for _, sample := range spoken {
		audio := speak(t, sample.phrase)

		started := time.Now()
		transcript, err := transcriber.Transcribe(ctx, audio)
		elapsed := time.Since(started)
		if err != nil {
			t.Errorf("%q: %v", sample.phrase, err)
			continue
		}

		match, rivals := commands.Match(transcript.Text)
		got := "no-match"
		if match != nil {
			got = match.Command
		} else if len(rivals) > 0 {
			got = "ambiguous"
		}

		t.Logf("  said %-24q heard %-30q -> %-14s  (%.2fs audio, %.2fs decode)",
			sample.phrase, transcript.Text, got,
			float64(len(audio))/float64(stt.SampleRate), elapsed.Seconds())

		if got != sample.want {
			t.Errorf("%q was routed to %s, want %s (heard %q)",
				sample.phrase, got, sample.want, transcript.Text)
		}

		// A command she has to wait several seconds for is one she will repeat,
		// which then arrives twice. Decoding should be well inside the pause
		// she leaves after speaking.
		if elapsed > 5*time.Second {
			t.Errorf("%q took %v to decode, which is too slow to feel responsive",
				sample.phrase, elapsed)
		}
	}
}
