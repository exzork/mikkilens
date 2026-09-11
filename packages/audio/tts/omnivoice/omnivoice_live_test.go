package omnivoice

import (
	"bytes"
	"context"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The live tests load two gigabytes of models and run them, so they are
// skipped unless MIKKILENS_LIVE=1. The normal run stays fast and needs nothing
// downloaded.
func liveOrSkip(t *testing.T) {
	t.Helper()
	if os.Getenv("MIKKILENS_LIVE") != "1" {
		t.Skip("set MIKKILENS_LIVE=1 to exercise OmniVoice")
	}
	if !Installed() {
		t.Skipf("OmniVoice is not installed in %s", Dir())
	}
}

// The decoder's frame rate is the number the whole length estimate is built
// on, and it is the one number here that was taken from documentation rather
// than from the files. If the decoder disagrees, every sentence is the wrong
// length and nothing else in this package can be trusted -- so ask it.
func TestDecoderAgreesAboutTheFrameRate(t *testing.T) {
	liveOrSkip(t)

	engine, err := Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer engine.Close()

	const frames = 40
	codes := make([][]int32, Codebooks)
	for row := range codes {
		codes[row] = make([]int32, frames)
		for column := range codes[row] {
			// Any real code will do; this is about how many samples come out,
			// not what they sound like.
			codes[row][column] = int32((row*7 + column*13) % 1024)
		}
	}

	samples, err := engine.synthesize(codes, frames)
	if err != nil {
		t.Fatalf("synthesize: %v", err)
	}

	perFrame := float64(len(samples)) / float64(frames)
	want := float64(SampleRate) / float64(FrameRate)
	if math.Abs(perFrame-want) > 1 {
		t.Errorf("the decoder produced %.1f samples per frame, want %.0f "+
			"(so FrameRate should be %.1f, not %d)",
			perFrame, want, float64(SampleRate)/perFrame, FrameRate)
	}
	t.Logf("%d frames decoded to %d samples (%.1f per frame, %.2fs of audio)",
		frames, len(samples), perFrame, float64(len(samples))/SampleRate)
}

// The end-to-end test: does it speak, and how long does it take to.
//
// The timing is logged rather than asserted. It is the number that decides
// whether this engine is usable for anything live, and it varies by an order
// of magnitude between machines, so a threshold here would be a test that
// fails on the wrong hardware rather than a fact about the code.
func TestSpeaksLive(t *testing.T) {
	liveOrSkip(t)

	engine, err := Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer engine.Close()

	// Which of the two very different speeds this run is about to show. The
	// gap between them is sixty-fold, so a timing below with no note of this
	// is a number nobody can interpret.
	t.Logf("running on the %s", map[bool]string{
		true: "graphics card", false: "processor"}[engine.Accelerated()])

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	const text = "Halo, ini contoh suara MikkiLens. Kamu sudah live."

	started := time.Now()
	samples, err := engine.Speak(ctx, text, Options{Language: "id"})
	took := time.Since(started)
	if err != nil {
		t.Fatalf("Speak: %v", err)
	}
	if len(samples) == 0 {
		t.Fatal("Speak produced no audio")
	}

	// The direction is spelled out rather than left to a ratio, because on a
	// card this comes out below one and "0.7x slower than real time" is a
	// sentence that means the opposite of what it says.
	seconds := float64(len(samples)) / SampleRate
	times, sense := took.Seconds()/seconds, "slower"
	if times < 1 {
		times, sense = 1/times, "faster"
	}
	t.Logf("%d samples, %.2fs of audio in %s (%.1fx %s than real time, %d steps)",
		len(samples), seconds, took.Round(time.Millisecond),
		times, sense, DefaultSteps)

	loud := 0
	for _, value := range samples {
		if math.Abs(float64(value)) > 0.01 {
			loud++
		}
	}
	// Silence is the failure mode worth catching: a pipeline wired almost
	// correctly produces the right number of samples and none of them sound
	// like anything.
	if loud < len(samples)/20 {
		t.Errorf("only %d of %d samples carry any signal; this is near-silence",
			loud, len(samples))
	}

	out := filepath.Join(t.TempDir(), "omnivoice.wav")
	if err := os.WriteFile(out, encodeWAV(samples), 0o644); err != nil {
		t.Fatalf("writing the sample: %v", err)
	}
	if kept := os.Getenv("MIKKILENS_KEEP_AUDIO"); kept != "" {
		if err := os.WriteFile(kept, encodeWAV(samples), 0o644); err == nil {
			t.Logf("wrote %s to listen to", kept)
		}
	}
}

// encodeWAV is here rather than in the package because nothing in the package
// needs it: the engine hands samples to the speech bus, which owns the output
// device. Only a person checking by ear needs a file.
func encodeWAV(samples []float32) []byte {
	body := &bytes.Buffer{}
	for _, value := range samples {
		clamped := math.Max(-1, math.Min(1, float64(value)))
		_ = binary.Write(body, binary.LittleEndian, int16(clamped*32767))
	}

	out := &bytes.Buffer{}
	out.WriteString("RIFF")
	_ = binary.Write(out, binary.LittleEndian, uint32(36+body.Len()))
	out.WriteString("WAVEfmt ")
	_ = binary.Write(out, binary.LittleEndian, uint32(16))
	_ = binary.Write(out, binary.LittleEndian, uint16(1))
	_ = binary.Write(out, binary.LittleEndian, uint16(1))
	_ = binary.Write(out, binary.LittleEndian, uint32(SampleRate))
	_ = binary.Write(out, binary.LittleEndian, uint32(SampleRate*2))
	_ = binary.Write(out, binary.LittleEndian, uint16(2))
	_ = binary.Write(out, binary.LittleEndian, uint16(16))
	out.WriteString("data")
	_ = binary.Write(out, binary.LittleEndian, uint32(body.Len()))
	out.Write(body.Bytes())
	return out.Bytes()
}
