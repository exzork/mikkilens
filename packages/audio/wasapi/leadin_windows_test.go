//go:build windows

package wasapi

import (
	"testing"
	"time"
)

// A Bluetooth headset drops the audio link when idle and takes a few hundred
// milliseconds to bring it back, losing whatever was sent meanwhile. The tail
// of a sound was already waited out for the same reason; this is the other
// end of it, and it is the more damaging one -- losing the first half second
// of a sentence loses the word that carries it.

func reset(t *testing.T) {
	t.Helper()
	leadInMu.Lock()
	leadIn, lastPlayed = 300*time.Millisecond, time.Time{}
	leadInMu.Unlock()
	t.Cleanup(func() {
		leadInMu.Lock()
		leadIn, lastPlayed = 300*time.Millisecond, time.Time{}
		leadInMu.Unlock()
	})
}

func TestAColdDeviceGetsSilenceFirst(t *testing.T) {
	reset(t)

	frames := leadInFrames(48000)
	if frames == 0 {
		t.Fatal("a device that has not played anything must be treated as asleep")
	}
	// 300ms at 48kHz.
	if wanted := uint32(0.3 * 48000); frames != wanted {
		t.Errorf("lead-in is %d frames, want %d", frames, wanted)
	}
}

// A sound following closely on another finds the link already up, and
// delaying it would add latency for nothing -- the earcon that says "I am
// listening" has to be immediate.
func TestAWarmDeviceGetsNoLeadIn(t *testing.T) {
	reset(t)
	markPlayed()

	if frames := leadInFrames(48000); frames != 0 {
		t.Errorf("lead-in is %d frames; a device still awake needs none", frames)
	}
}

func TestADeviceGoesColdAgainAfterAWhile(t *testing.T) {
	reset(t)

	leadInMu.Lock()
	lastPlayed = time.Now().Add(-2 * warmWindow)
	leadInMu.Unlock()

	if frames := leadInFrames(48000); frames == 0 {
		t.Error("after this long the link has dropped and the lead-in is needed again")
	}
}

// Wired output needs none of this, and someone who turns it off must actually
// get zero rather than a floor.
func TestSettingZeroDisablesTheLeadInEntirely(t *testing.T) {
	reset(t)
	SetLeadIn(0)

	if frames := leadInFrames(48000); frames != 0 {
		t.Errorf("lead-in is %d frames, want none", frames)
	}
}

func TestANegativeLeadInIsTreatedAsOff(t *testing.T) {
	reset(t)
	SetLeadIn(-5 * time.Second)

	if frames := leadInFrames(48000); frames != 0 {
		t.Errorf("lead-in is %d frames; a negative setting must not wrap around", frames)
	}
}

func TestTheLeadInScalesWithTheDeviceSampleRate(t *testing.T) {
	reset(t)
	SetLeadIn(500 * time.Millisecond)

	at44k := leadInFrames(44100)
	reset(t)
	SetLeadIn(500 * time.Millisecond)
	at48k := leadInFrames(48000)

	if at44k >= at48k {
		t.Errorf("same duration gave %d frames at 44.1kHz and %d at 48kHz; "+
			"the lead-in is a duration, not a frame count", at44k, at48k)
	}
	if wanted := uint32(0.5 * 44100); at44k != wanted {
		t.Errorf("lead-in at 44.1kHz is %d frames, want %d", at44k, wanted)
	}
}
