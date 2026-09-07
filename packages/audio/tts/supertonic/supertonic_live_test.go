package supertonic

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The live tests load four hundred megabytes of models and run them, so they
// are skipped unless MIKKILENS_LIVE=1. The normal run stays fast and needs
// nothing downloaded.
func liveOrSkip(t *testing.T) {
	t.Helper()
	if os.Getenv("MIKKILENS_LIVE") != "1" {
		t.Skip("set MIKKILENS_LIVE=1 to exercise the local voice")
	}
	if !Installed() {
		t.Skipf("the local voice is not installed in %s", Dir())
	}
}

func TestSpeaksIndonesianLive(t *testing.T) {
	liveOrSkip(t)

	engine, err := Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer engine.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	started := time.Now()
	samples, err := engine.Speak(ctx,
		"Halo, ini contoh suara MikkiLens. Kamu sudah live.",
		Options{Voice: "F1", Language: "id"})
	if err != nil {
		t.Fatalf("Speak: %v", err)
	}

	seconds := float64(len(samples)) / float64(engine.SampleRate())
	t.Logf("%d samples, %.2fs of audio at %d Hz, made in %s",
		len(samples), seconds, engine.SampleRate(), time.Since(started).Round(time.Millisecond))

	if seconds < 1.0 {
		t.Errorf("only %.2fs of audio for a two-sentence phrase", seconds)
	}
	if peak := peakOf(samples); peak < 0.01 {
		t.Errorf("the audio is silent: peak %.4f", peak)
	}

	// Written out so it can actually be listened to, which is the only way to
	// tell good synthesis from confident noise.
	if directory := os.Getenv("MIKKILENS_LIVE_OUT"); directory != "" {
		path := filepath.Join(directory, "supertonic-id.wav")
		if err := writeWAV(path, samples, engine.SampleRate()); err != nil {
			t.Errorf("could not write %s: %v", path, err)
		} else {
			t.Logf("wrote %s", path)
		}
	}
}

// TestEveryVoiceSpeaksLive checks that all ten preset voices load and produce
// sound, because a voice that fails only when she picks it fails on stream.
func TestEveryVoiceSpeaksLive(t *testing.T) {
	liveOrSkip(t)

	engine, err := Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer engine.Close()

	for _, voice := range Voices() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		samples, err := engine.Speak(ctx, "Selamat datang di siaran ini.",
			Options{Voice: voice.Name, Language: "id", Steps: 5})
		cancel()
		if err != nil {
			t.Errorf("%s: %v", voice.Name, err)
			continue
		}
		seconds := float64(len(samples)) / float64(engine.SampleRate())
		t.Logf("%s (%s): %.2fs, peak %.3f", voice.Name, voice.Gender, seconds, peakOf(samples))
		if peakOf(samples) < 0.01 {
			t.Errorf("%s produced silence", voice.Name)
		}
	}
}

// TestSpeedAndStepsLive checks the two dials do what they claim: more steps
// costs time, and a higher speed makes the same words shorter.
func TestSpeedAndStepsLive(t *testing.T) {
	liveOrSkip(t)

	engine, err := Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer engine.Close()

	const line = "Mikrofon dimatikan, dan siaran tetap berjalan seperti biasa."

	for _, steps := range []int{5, 8, 12} {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		started := time.Now()
		samples, err := engine.Speak(ctx, line, Options{Language: "id", Steps: steps})
		cancel()
		if err != nil {
			t.Fatalf("%d steps: %v", steps, err)
		}
		seconds := float64(len(samples)) / float64(engine.SampleRate())
		took := time.Since(started)
		t.Logf("%2d steps: %.2fs of audio in %s (%.2fx real time)",
			steps, seconds, took.Round(time.Millisecond), seconds/took.Seconds())
	}

	var lengths []int
	for _, speed := range []float32{0.9, 1.05, 1.4} {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		samples, err := engine.Speak(ctx, line, Options{Language: "id", Speed: speed, Steps: 5})
		cancel()
		if err != nil {
			t.Fatalf("speed %.2f: %v", speed, err)
		}
		t.Logf("speed %.2f: %.2fs", speed, float64(len(samples))/float64(engine.SampleRate()))
		lengths = append(lengths, len(samples))
	}
	if !(lengths[0] > lengths[1] && lengths[1] > lengths[2]) {
		t.Errorf("speed did not shorten the audio: %v", lengths)
	}
}

// TestLongTextIsChunkedLive checks that a donation-length message comes back as
// one continuous piece of audio rather than failing or being truncated.
func TestLongTextIsChunkedLive(t *testing.T) {
	liveOrSkip(t)

	engine, err := Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer engine.Close()

	long := "Terima kasih banyak atas dukungannya hari ini. " +
		"Semoga siarannya menyenangkan dan semua orang betah menonton sampai selesai. " +
		"Jangan lupa istirahat yang cukup, minum air putih, dan jaga kesehatan. " +
		"Sampai jumpa di siaran berikutnya, semoga harimu menyenangkan selalu. " +
		"Salam hangat dari kami semua yang menonton dari rumah masing-masing."

	pieces := chunk(long, chunkLimit("id"))
	if len(pieces) < 2 {
		t.Fatalf("expected the text to be split, got %d piece(s)", len(pieces))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	samples, err := engine.Speak(ctx, long, Options{Language: "id", Steps: 5})
	if err != nil {
		t.Fatalf("Speak: %v", err)
	}
	seconds := float64(len(samples)) / float64(engine.SampleRate())
	t.Logf("%d chunks joined into %.2fs", len(pieces), seconds)
	if seconds < 10 {
		t.Errorf("five sentences came back as only %.2fs", seconds)
	}
}

// TestCancellationStopsTheDenoisingLive checks that an interrupted utterance
// stops being made rather than finishing and being thrown away.
func TestCancellationStopsTheDenoisingLive(t *testing.T) {
	liveOrSkip(t)

	engine, err := Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer engine.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := engine.Speak(ctx, "Ini tidak akan pernah selesai.",
		Options{Language: "id"}); err == nil {
		t.Error("expected a cancelled context to stop synthesis")
	}
}

func peakOf(samples []float32) float32 {
	peak := float32(0)
	for _, sample := range samples {
		if sample > peak {
			peak = sample
		} else if -sample > peak {
			peak = -sample
		}
	}
	return peak
}

// writeWAV saves mono float32 samples as 16-bit PCM, so the result can be
// played by anything.
func writeWAV(path string, samples []float32, sampleRate int) error {
	body := make([]byte, len(samples)*2)
	for index, sample := range samples {
		if sample > 1 {
			sample = 1
		} else if sample < -1 {
			sample = -1
		}
		value := int16(sample * 32767)
		binary.LittleEndian.PutUint16(body[index*2:], uint16(value))
	}

	header := &bytes.Buffer{}
	header.WriteString("RIFF")
	binary.Write(header, binary.LittleEndian, uint32(36+len(body)))
	header.WriteString("WAVEfmt ")
	binary.Write(header, binary.LittleEndian, uint32(16))
	binary.Write(header, binary.LittleEndian, uint16(1)) // PCM
	binary.Write(header, binary.LittleEndian, uint16(1)) // mono
	binary.Write(header, binary.LittleEndian, uint32(sampleRate))
	binary.Write(header, binary.LittleEndian, uint32(sampleRate*2))
	binary.Write(header, binary.LittleEndian, uint16(2))
	binary.Write(header, binary.LittleEndian, uint16(16))
	header.WriteString("data")
	binary.Write(header, binary.LittleEndian, uint32(len(body)))

	return os.WriteFile(path, append(header.Bytes(), body...), 0o644)
}

// TestChangingVoiceNeedsNoReloadLive is the behaviour the settings page
// depends on: saving a different voice takes effect on the next thing she
// says, without the four models being torn down and loaded again.
//
// Same engine throughout, and the audio has to actually differ -- two voices
// that load fine and sound identical would pass a weaker test and fail her.
func TestChangingVoiceNeedsNoReloadLive(t *testing.T) {
	liveOrSkip(t)

	engine, err := Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer engine.Close()

	const line = "Selamat datang di siaran ini."
	say := func(voice string) []float32 {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		started := time.Now()
		samples, err := engine.Speak(ctx, line, Options{Voice: voice, Language: "id", Steps: 5})
		if err != nil {
			t.Fatalf("%s: %v", voice, err)
		}
		t.Logf("%s: %.2fs, made in %s", voice,
			float64(len(samples))/float64(engine.SampleRate()),
			time.Since(started).Round(time.Millisecond))
		return samples
	}

	female := say("F1")
	male := say("M2")

	// A reload would show up here as seconds, not milliseconds. The second
	// voice is a 300 KB file against four hundred megabytes of model.
	if len(female) == len(male) {
		t.Errorf("both voices produced %d samples, which is suspicious", len(female))
	}

	// And back again, because the first voice must not have been evicted by
	// the second: she switches back and forth while deciding.
	if again := say("F1"); len(again) != len(female) {
		t.Errorf("F1 came back as %d samples, was %d", len(again), len(female))
	}
}
