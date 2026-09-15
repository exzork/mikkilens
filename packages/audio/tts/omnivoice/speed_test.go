package omnivoice

import "testing"

// Speed is the one option here that can quietly damage what she hears.
//
// It is not a pitch-preserving speed-up: it divides the number of frames the
// model has to fill, so too large a value is answered by slurring and by the
// sentence stopping before its last word -- at confident volume, sounding
// finished. That is why the clamp lives in speed(), which every caller passes
// through, rather than where a rate string happens to be parsed.

func TestASpeedPastWhatTheModelCanSayIsClamped(t *testing.T) {
	// +35%, which is what a chat rate tuned for a different engine arrives as.
	if got := (Options{Speed: 1.35}).speed(); got != MaxSpeed {
		t.Errorf("speed() = %v for 1.35, want it held at %v", got, MaxSpeed)
	}
	if got := (Options{Speed: 4}).speed(); got != MaxSpeed {
		t.Errorf("speed() = %v for 4, want it held at %v", got, MaxSpeed)
	}
}

func TestASpeedWithinReachIsLeftAlone(t *testing.T) {
	for _, want := range []float32{0.5, 1, 1.1, MaxSpeed} {
		if got := (Options{Speed: want}).speed(); got != want {
			t.Errorf("speed() = %v for %v, want it untouched", got, want)
		}
	}
}

// Unset means ordinary speed rather than silence. A zero reaching the frame
// estimate would ask for no frames at all.
func TestAnUnsetSpeedIsOrdinarySpeed(t *testing.T) {
	for _, given := range []float32{0, -1} {
		if got := (Options{Speed: given}).speed(); got != 1 {
			t.Errorf("speed() = %v for %v, want 1", got, given)
		}
	}
}

// Slower is not capped: more frames to fill is something the model does
// comfortably, and a floor here would be a limit nobody measured.
func TestSlowerThanOrdinaryIsNotCapped(t *testing.T) {
	if got := (Options{Speed: 0.25}).speed(); got != 0.25 {
		t.Errorf("speed() = %v for 0.25, want it untouched", got)
	}
}
