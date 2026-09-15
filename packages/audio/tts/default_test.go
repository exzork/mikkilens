package tts

import (
	"testing"

	"github.com/exzork/mikkilens/packages/audio/tts/omnivoice"
	"github.com/exzork/mikkilens/packages/core/config"
)

// Out of the box MikkiLens reads in her own voice. The config package spells
// both names out rather than importing them, because it sits under the audio
// packages; this is what stops the two drifting apart into a default voice
// that is not the one that ships.
func TestTheDefaultIsHerOwnVoice(t *testing.T) {
	speech := config.Default().Speech
	if speech.Engine != EngineOmni {
		t.Errorf("default engine = %q, want %q", speech.Engine, EngineOmni)
	}
	if speech.Voice != omnivoice.DefaultVoice {
		t.Errorf("default voice = %q, want the built-in %q", speech.Voice, omnivoice.DefaultVoice)
	}
}
