package wake

import (
	"testing"
	"time"
)

// Her name is the wake word, so every sentence MikkiLens speaks that contains
// it comes out of the speakers and back into the microphone, where the
// detector has no way of telling that voice from hers. These pin the switch
// that stops it scoring MikkiLens against its own name.
//
// Internal because what is worth asserting is the state itself: driving real
// audio through would test the model rather than the switch.

// Her name is the wake word, so every sentence MikkiLens speaks that contains
// it comes out of the speakers and back into the microphone, where the
// detector has no way of telling that voice from hers. These pin the switch
// that stops it scoring MikkiLens against its own name.

func TestNothingIsScoredWhileMikkiLensIsSpeaking(t *testing.T) {
	detector := New(Options{Model: "mikkilens", Threshold: 0.5})
	detector.Resume()

	if !detector.listening() {
		t.Fatal("a resumed detector must be listening to begin with")
	}
	detector.SetSpeaking(true)
	if detector.listening() {
		t.Error("the detector is still scoring while the speakers are carrying speech")
	}
}

// The scoring window is over a second wide, so at the moment playback stops the
// tail of her own name is still inside it. Resuming immediately would score
// that tail and fire on it.
func TestTheDetectorStaysOffBrieflyAfterSpeaking(t *testing.T) {
	detector := New(Options{Model: "mikkilens", Threshold: 0.5})
	detector.Resume()

	detector.SetSpeaking(true)
	detector.SetSpeaking(false)
	if detector.listening() {
		t.Error("listening resumed the instant the speakers went quiet")
	}

	detector.mu.Lock()
	detector.quietUntil = time.Now().Add(-time.Millisecond)
	detector.mu.Unlock()
	if !detector.listening() {
		t.Error("the detector never came back after the tail elapsed")
	}
}

// Speaking and recording a command are two different reasons to be switched
// off, and they overlap: she speaks, and MikkiLens answers before she has
// finished. Whichever ends first must not switch listening back on underneath
// the other.
func TestSpeakingAndRecordingDoNotUnmuteEachOther(t *testing.T) {
	detector := New(Options{Model: "mikkilens", Threshold: 0.5})
	detector.Resume()

	detector.SetSpeaking(true)
	detector.Pause() // a command starts being recorded while it is still talking

	detector.SetSpeaking(false)
	detector.mu.Lock()
	detector.quietUntil = time.Now().Add(-time.Millisecond)
	detector.mu.Unlock()
	if detector.listening() {
		t.Error("the end of speech switched listening back on mid-recording")
	}

	detector.Resume() // the recording finishes
	if !detector.listening() {
		t.Error("the detector never came back once both reasons had passed")
	}
}
