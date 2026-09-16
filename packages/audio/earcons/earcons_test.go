package earcons

import "testing"

// An earcon shorter than minimumSeconds is not a quieter acknowledgement, it
// is no acknowledgement at all: a Bluetooth headset renders nothing for it.
// The listening tone was 90 ms and was never heard on one, which is the whole
// reason this is a test rather than a comment.
func TestEveryEarconIsLongEnoughToBeHeard(t *testing.T) {
	for _, name := range Names() {
		wave, err := Render(name, 1.0)
		if err != nil {
			t.Fatalf("Render(%q): %v", name, err)
		}
		seconds := float64(len(wave)) / SampleRate
		if seconds < minimumSeconds {
			t.Errorf("%q is %.0f ms, under the %.0f ms a Bluetooth headset needs",
				name, seconds*1000, minimumSeconds*1000)
		}
	}
}

// The lengths in the patterns are what Render actually produces, gaps
// included. Worth pinning because the check above would pass just as happily
// on a tone that was accidentally rendered twice.
func TestRenderedLengthMatchesThePattern(t *testing.T) {
	for name, pattern := range patterns {
		wanted := 0.0
		for index, tone := range pattern {
			if index > 0 {
				wanted += gapSeconds
			}
			wanted += tone.duration
		}

		wave, err := Render(name, 1.0)
		if err != nil {
			t.Fatalf("Render(%q): %v", name, err)
		}
		seconds := float64(len(wave)) / SampleRate
		if difference := seconds - wanted; difference > 0.002 || difference < -0.002 {
			t.Errorf("%q renders %.0f ms, want %.0f ms", name, seconds*1000, wanted*1000)
		}
	}
}
