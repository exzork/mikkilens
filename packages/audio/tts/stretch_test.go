package tts

import (
	"math"
	"testing"
)

// tone is a plain sine, which is what makes the pitch measurable: count how
// often it crosses zero and the frequency falls out.
func tone(frequency float64, seconds float64, sampleRate int) []float32 {
	samples := make([]float32, int(float64(sampleRate)*seconds))
	step := 2 * math.Pi * frequency / float64(sampleRate)
	for index := range samples {
		samples[index] = float32(math.Sin(step * float64(index)))
	}
	return samples
}

// crossings per second, which for a sine is twice its frequency.
func pitchOf(samples []float32, sampleRate int) float64 {
	crossings := 0
	for index := 1; index < len(samples); index++ {
		if (samples[index-1] < 0) != (samples[index] < 0) {
			crossings++
		}
	}
	seconds := float64(len(samples)) / float64(sampleRate)
	return float64(crossings) / seconds / 2
}

const stretchRate = 24000 // what OmniVoice produces

// The whole point: shorter, and in the same voice. A resample would do the
// first and ruin the second.
func TestStretchingShortensWithoutMovingThePitch(t *testing.T) {
	original := tone(220, 2, stretchRate)
	before := pitchOf(original, stretchRate)

	for _, speed := range []float32{1.15, 1.5, 2.0} {
		faster := stretch(original, stretchRate, speed)

		wanted := float64(len(original)) / float64(speed)
		if difference := math.Abs(float64(len(faster))-wanted) / wanted; difference > 0.05 {
			t.Errorf("at %.2fx the audio is %d samples, want about %.0f",
				speed, len(faster), wanted)
		}

		after := pitchOf(faster, stretchRate)
		if difference := math.Abs(after-before) / before; difference > 0.05 {
			t.Errorf("at %.2fx the pitch moved from %.0f Hz to %.0f Hz", speed, before, after)
		}
	}
}

// Ordinary speed is the common case and must cost nothing.
func TestStretchingAtOrdinarySpeedChangesNothing(t *testing.T) {
	original := tone(220, 1, stretchRate)
	for _, speed := range []float32{0, 1, 0.8, 1.001} {
		if got := stretch(original, stretchRate, speed); len(got) != len(original) {
			t.Errorf("at %v the audio came back %d samples, want %d untouched",
				speed, len(got), len(original))
		}
	}
}

// Past twice speed the overlaps land on different sounds however well they are
// aligned, so it holds there rather than turning speech into a stutter.
func TestStretchingHoldsAtItsCeiling(t *testing.T) {
	original := tone(220, 2, stretchRate)
	atMax := stretch(original, stretchRate, stretchMax)
	beyond := stretch(original, stretchRate, 4)

	if math.Abs(float64(len(beyond)-len(atMax))) > float64(stretchRate)/10 {
		t.Errorf("4x gave %d samples and the ceiling %d; it is not being held",
			len(beyond), len(atMax))
	}
}

// A confirmation is often a word and a half, which is shorter than the pieces
// this works on. It has to come back whole rather than mangled.
func TestStretchingLeavesVeryShortAudioAlone(t *testing.T) {
	short := tone(220, 0.05, stretchRate)
	if got := stretch(short, stretchRate, 1.5); len(got) != len(short) {
		t.Errorf("a %d sample clip came back %d", len(short), len(got))
	}
	if got := stretch(nil, stretchRate, 1.5); len(got) != 0 {
		t.Errorf("nothing came back as %d samples", len(got))
	}
}
