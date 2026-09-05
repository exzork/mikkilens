package tts

import "testing"

// The volume is applied to the samples on their way to the speaker rather than
// asked of the voice that made them, which is what makes it hold for cached
// speech, for the offline voice, and from the moment she changes it.

func TestAtVolumeScalesTheSamples(t *testing.T) {
	audio := Audio{Samples: []float32{1, -1, 0.5}, SampleRate: 48000, Channels: 1}

	quieter := audio.AtVolume(50)
	want := []float32{0.5, -0.5, 0.25}
	for index, sample := range quieter.Samples {
		if sample != want[index] {
			t.Errorf("sample %d = %v, want %v", index, sample, want[index])
		}
	}
}

// The cache hands the same samples out again and again, so scaling has to copy
// rather than write through. Turning her down once and having every cached
// phrase quieter for good -- and quieter again the next time -- is the failure
// this prevents.
func TestAtVolumeLeavesTheOriginalAlone(t *testing.T) {
	audio := Audio{Samples: []float32{1, 1}, SampleRate: 48000, Channels: 1}
	audio.AtVolume(25)

	for index, sample := range audio.Samples {
		if sample != 1 {
			t.Errorf("the original sample %d became %v", index, sample)
		}
	}
}

// Full volume is the ordinary case and copies nothing. Zero is silence of the
// same length rather than no audio at all, so playback still takes the time it
// would have taken.
func TestAtVolumeAtTheEnds(t *testing.T) {
	audio := Audio{Samples: []float32{1, 1}, SampleRate: 48000, Channels: 1}

	if full := audio.AtVolume(100); &full.Samples[0] != &audio.Samples[0] {
		t.Error("full volume copied the samples")
	}
	silent := audio.AtVolume(0)
	if len(silent.Samples) != len(audio.Samples) {
		t.Fatalf("silence is %d samples, want %d", len(silent.Samples), len(audio.Samples))
	}
	for index, sample := range silent.Samples {
		if sample != 0 {
			t.Errorf("silent sample %d = %v", index, sample)
		}
	}
}
