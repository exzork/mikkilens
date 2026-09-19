package silero

import (
	"encoding/binary"
	"math/rand"
	"os"
	"testing"

	"github.com/exzork/mikkilens/packages/audio/onnx"
)

// The recordings that came back from Whisper as "Terima kasih kerana menonton"
// were two seconds of nothing: silence, hiss, a hum. Those must count as no
// speech, and a spoken sentence must not.

func detector(t *testing.T) *Detector {
	t.Helper()
	if err := onnx.Start(); err != nil {
		t.Skipf("no ONNX runtime on this machine: %v", err)
	}
	return &Detector{}
}

func readWAV(t *testing.T, name string) []float32 {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	pcm := data[44:] // a plain 16-bit mono header
	samples := make([]float32, len(pcm)/2)
	for index := range samples {
		samples[index] = float32(int16(binary.LittleEndian.Uint16(pcm[index*2:]))) / 32768
	}
	return samples
}

func TestSpeechIsHeardAsSpeech(t *testing.T) {
	seconds, err := detector(t).SpeechSeconds(readWAV(t, "speech.wav"))
	if err != nil {
		t.Fatal(err)
	}
	if seconds < 1 {
		t.Errorf("a spoken sentence has %.2fs of speech, want most of it", seconds)
	}
}

func TestNothingIsHeardAsNothing(t *testing.T) {
	random := rand.New(rand.NewSource(7))
	noise := func(level float32) []float32 {
		samples := make([]float32, 2*SampleRate)
		for index := range samples {
			samples[index] = float32(random.NormFloat64()) * level
		}
		return samples
	}

	d := detector(t)
	for name, audio := range map[string][]float32{
		"silence":    make([]float32, 2*SampleRate),
		"quiet hiss": noise(0.003),
		"hiss":       noise(0.02),
	} {
		seconds, err := d.SpeechSeconds(audio)
		if err != nil {
			t.Fatal(err)
		}
		if seconds > 0.1 {
			t.Errorf("%s has %.2fs of speech, want none", name, seconds)
		}
	}
}
