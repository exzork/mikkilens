package engine

import (
	"testing"

	"github.com/exzork/mikkilens/packages/audio/wake"
)

// Going deaf while MikkiLens talks is a gate with one purpose: her name is the
// trigger, and a sentence saying it comes back through the microphone. Applied
// to everything she says it costs her the microphone for the whole of chat,
// which is most of a stream and exactly when "jeda chat" is wanted.
//
// So two things are worth pinning. Which sentences close the gate, and that
// opening it again only happens for the sentences that closed it -- coming
// back carries a tail of deafness, so an unmatched open is deafness bought for
// nothing at the end of every utterance.

func TestOnlySpeechThatSaysHerNameClosesTheGate(t *testing.T) {
	for _, test := range []struct {
		name string
		text string
		word string
		want bool
	}{
		// Chat, which is the case this exists for. None of it can trigger a
		// detector listening for her name.
		{"an ordinary chat message", "budi123: halo kak semangat terus", "mikkilens", false},
		{"a chat message about the game", "andi: itu bossnya susah banget", "mikkilens", false},
		{"a song title", "Yorushika - Say It", "mikkilens", false},
		{"a plain result", "Sekarang jam 14:35.", "mikkilens", false},

		// And the ones that really can set it off.
		{"a viewer saying her name", "rina: mikkilens lucu banget", "mikkilens", true},
		{"reading a command back", "MikkiLens mendengarkan", "mikkilens", true},
		{"her name with punctuation after it", "MikkiLens, halo!", "mikkilens", true},
		{"her name mid-sentence", "kata MikkiLens tadi begitu", "mikkilens", true},

		// The wake word is a model name and the text is a sentence, so the two
		// are spelled differently on purpose.
		{"an underscored model name", "hey mikki, tolong", "hey_mikki", true},
		{"a hyphenated model name", "Hey Mikki!", "hey-mikki", true},
		{"a model name that is not said", "halo semuanya", "hey_mikki", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := mentionsWakeWord(test.text, test.word); got != test.want {
				t.Errorf("mentionsWakeWord(%q, %q) is %v, want %v",
					test.text, test.word, got, test.want)
			}
		})
	}
}

// Case and punctuation are the normal state of the text, not the exception:
// it is a sentence read aloud, and her name arrives capitalised with a comma
// after it.
func TestHerNameIsFoundHoweverItIsWritten(t *testing.T) {
	for _, text := range []string{
		"mikkilens", "MikkiLens", "MIKKILENS", "Mikkilens,", "...MikkiLens?",
		"oi, mikkilens!", "MikkiLens: halo",
	} {
		if !mentionsWakeWord(text, "mikkilens") {
			t.Errorf("%q was not recognised as saying her name", text)
		}
	}
}

// Without a wake word configured there is nothing to compare against, and
// deafness is the safe answer: a detector firing on MikkiLens's own voice is
// worse than one that waits.
func TestAnEmptyWakeWordClosesTheGate(t *testing.T) {
	if !mentionsWakeWord("halo semuanya", "") {
		t.Error("an unknown wake word must close the gate rather than open it")
	}
	if !mentionsWakeWord("halo semuanya", "   ") {
		t.Error("a blank wake word must close the gate rather than open it")
	}
}

// The regression this guards. Coming back from speaking imposes a tail of
// deafness, so calling it for an utterance the gate was never closed for would
// make her deaf for the tail at the end of every sentence MikkiLens speaks --
// which is worse than the behaviour being fixed.
func TestTheGateOnlyOpensForSpeechItClosedFor(t *testing.T) {
	detector := wake.New(wake.Options{Model: "mikkilens", Threshold: 0.5})
	detector.Resume()

	engine := &Engine{wake: detector}

	// A sentence that never closed the gate.
	engine.wakeGated.Store(false)
	engine.gateWakeWord(false)
	if detector.Speaking() {
		t.Error("the detector was put into speaking by an utterance that never gated it")
	}
	if engine.wakeGated.Load() {
		t.Error("the flag survived an ungated utterance")
	}

	// One that did.
	detector.SetSpeaking(true)
	engine.wakeGated.Store(true)
	engine.gateWakeWord(false)
	if detector.Speaking() {
		t.Error("the gate never opened again after the speech it was closed for")
	}
	if engine.wakeGated.Load() {
		t.Error("the flag was not cleared once the gate opened")
	}
}

// Without a detector there is nothing to gate, and the hook still runs on
// every utterance -- so it has to be a no-op rather than a crash mid-stream.
func TestGatingWithoutADetectorDoesNothing(t *testing.T) {
	engine := &Engine{}
	engine.gateWakeWord(true)
	engine.gateWakeWord(false)
}
