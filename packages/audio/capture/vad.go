package capture

import "math"

// VAD decides which 30 ms frames carry speech, which is what ends an utterance
// on a pause rather than on a timer.
//
// The Python version leaned on WebRTC's VAD, a C library with a Gaussian model
// over six sub-bands. This is an adaptive energy detector instead: it tracks
// the room's noise floor and calls a frame speech when it rises far enough
// above it, with hysteresis so one quiet syllable does not end the sentence.
//
// The trade is real and worth stating. WebRTC is better at picking a voice out
// of loud broadband noise. This is better at adapting to a room that changes
// -- a fan starting, a game getting louder -- because the floor keeps moving,
// and it needs no C toolchain to build. For deciding when someone has stopped
// speaking into a close microphone, which is all it is asked to do, energy is
// enough.
type VAD struct {
	// factor is how far above the noise floor a frame has to be. Higher is
	// stricter, which trims silence harder and cuts speech sooner.
	factor float64

	// absoluteFloor stops a silent room, where the noise floor tends to zero,
	// from calling faint hiss speech.
	absoluteFloor float64

	noiseFloor float64
	primed     bool
	inSpeech   bool
}

// NewVAD builds a detector. Aggressiveness runs 0 to 3, matching the knob the
// config has always exposed: higher trims silence more aggressively.
func NewVAD(aggressiveness int) *VAD {
	aggressiveness = max(0, min(3, aggressiveness))
	factors := []float64{2.0, 2.8, 3.8, 5.5}
	floors := []float64{0.0030, 0.0040, 0.0055, 0.0080}
	return &VAD{
		factor:        factors[aggressiveness],
		absoluteFloor: floors[aggressiveness],
	}
}

// Reset forgets the room, which matters between utterances: the floor learned
// while she was talking is not the floor of the silence that follows.
func (v *VAD) Reset() {
	v.noiseFloor = 0
	v.primed = false
	v.inSpeech = false
}

// IsSpeech reports whether one frame of 16 kHz mono audio carries speech.
func (v *VAD) IsSpeech(frame []float32) bool {
	if len(frame) == 0 {
		return false
	}
	level := rms(frame)

	if !v.primed {
		v.noiseFloor = level
		v.primed = true
	}

	threshold := math.Max(v.noiseFloor*v.factor, v.absoluteFloor)
	// Hysteresis: once speech has started it takes a clearly quieter frame to
	// end it, so an unvoiced consonant mid-word does not read as a pause.
	if v.inSpeech {
		threshold *= 0.6
	}
	speech := level > threshold

	if !speech {
		// Track the floor upward slowly and downward quickly, so a room that
		// gets noisier is followed without a brief noise raising it for good.
		if level > v.noiseFloor {
			v.noiseFloor += (level - v.noiseFloor) * 0.05
		} else {
			v.noiseFloor += (level - v.noiseFloor) * 0.35
		}
	}
	v.inSpeech = speech
	return speech
}

// Level is the current loudness of a frame, 0 to 1. The setup wizard uses it
// to tell "the microphone works" from "the microphone is muted".
func Level(frame []float32) float64 { return rms(frame) }

func rms(frame []float32) float64 {
	if len(frame) == 0 {
		return 0
	}
	total := 0.0
	for _, sample := range frame {
		total += float64(sample) * float64(sample)
	}
	return math.Sqrt(total / float64(len(frame)))
}
