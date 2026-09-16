package tts

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/exzork/mikkilens/packages/audio/tts/omnivoice"
	"github.com/exzork/mikkilens/packages/core/paths"
)

func TestOmniVoiceIsAnEngine(t *testing.T) {
	if got := resolveEngine(EngineOmni); got != EngineOmni {
		t.Errorf("resolveEngine(%q) = %q", EngineOmni, got)
	}

	found := false
	for _, engine := range Engines {
		if engine == EngineOmni {
			found = true
		}
	}
	if !found {
		t.Errorf("%q is not in Engines, so the settings page will never offer it", EngineOmni)
	}
}

// Everything stands behind OmniVoice; OmniVoice stands behind nothing.
//
// Without a graphics card it is roughly twenty-five times slower than real
// time, so landing on it by accident turns a missing confirmation into one
// that arrives half a minute late -- which on a live stream is worse than the
// one that never came.
func TestOmniVoiceIsNeverASubstitute(t *testing.T) {
	for _, chosen := range []string{EngineLocal, EngineOnline, EngineWindows, ""} {
		for _, engine := range fallbackOrder(chosen) {
			if engine == EngineOmni {
				t.Errorf("choosing %q can fall back to OmniVoice: %v",
					chosen, fallbackOrder(chosen))
			}
		}
	}

	order := fallbackOrder(EngineOmni)
	if len(order) == 0 || order[0] != EngineOmni {
		t.Fatalf("choosing OmniVoice starts with %v", order)
	}
	if order[len(order)-1] != EngineWindows {
		t.Errorf("OmniVoice's fallbacks end at %q, not the floor", order[len(order)-1])
	}
}

// The same rate string has to mean the same thing to every engine, or the
// setting is four settings wearing one label.
func TestOmniSpeedReadsTheRateTheWayEveryEngineDoes(t *testing.T) {
	for _, test := range []struct {
		rate string
		want float32
	}{
		{"+0%", 1},
		{"", 1},
		{"+25%", 1.25},
		{"-50%", 0.5},
		{"+100%", 2},
		// A hand-edited file with a typo in it must not be the reason
		// MikkiLens stops talking.
		{"quickly", 1},
	} {
		if got := omniSpeed(test.rate); got != test.want {
			t.Errorf("omniSpeed(%q) = %v, want %v", test.rate, got, test.want)
		}
	}
}

// The rate is applied to the audio after it has been spoken, not asked of the
// model, so the ceiling is where overlapping pieces of speech stop landing on
// the same sound rather than where the model starts dropping words.

func TestTheOmniCeilingIsTheLastRateThatWorks(t *testing.T) {
	ceiling := omniSpeed(fmt.Sprintf("+%d%%", OmniSpeedCeiling()))
	if ceiling > stretchMax+0.001 {
		t.Errorf("the ceiling rate maps to %v, past the %v speeding up holds at",
			ceiling, stretchMax)
	}
	// And just past it, the number she is told is the last one that works.
	beyond := omniSpeed(fmt.Sprintf("+%d%%", OmniSpeedCeiling()+1))
	if beyond <= stretchMax {
		t.Errorf("+%d%% maps to %v, still within %v -- the ceiling is being "+
			"reported lower than it is", OmniSpeedCeiling()+1, beyond, stretchMax)
	}
}

// The ceiling is quoted to her as a rate she can actually set, so it has to be
// a whole percentage that is not itself clamped.
func TestTheOmniCeilingIsAWholeUsableRate(t *testing.T) {
	if got := OmniSpeedCeiling(); got != 100 {
		t.Errorf("OmniSpeedCeiling() = %d, want 100", got)
	}
}

// The model keeps its own guard for anything that still sets its speed
// directly, which is what a test or a future caller would do.
func TestTheModelKeepsItsOwnSpeedGuard(t *testing.T) {
	if omnivoice.MaxSpeed >= stretchMax {
		t.Errorf("the model's guard is %v, no longer below the %v the audio is "+
			"sped up to -- rushing the model is back on the table",
			omnivoice.MaxSpeed, stretchMax)
	}
}

// The engine and the voice both go into the cache key, and OmniVoice shares a
// naming scheme with nothing -- a voice called "mikki" under a different engine
// is different audio, and serving one for the other would be heard as the
// setting having done nothing.
func TestOmniVoiceHasItsOwnCacheEntries(t *testing.T) {
	omni := cacheKey("Halo", Options{Engine: EngineOmni, Voice: "mikki"})
	local := cacheKey("Halo", Options{Engine: EngineLocal, Voice: "mikki"})
	if omni == local {
		t.Error("the same voice name under two engines shares a cache entry")
	}

	other := cacheKey("Halo", Options{Engine: EngineOmni, Voice: "budi"})
	if omni == other {
		t.Error("two different OmniVoice voices share a cache entry")
	}
}

// Saving a new recording under a voice's existing name is how a voice gets
// replaced, and the cache has to notice. It did not, once: every phrase already
// said went on playing in the voice that had just been thrown away.
func TestReplacingAnOmniVoiceMissesTheCache(t *testing.T) {
	root := t.TempDir()
	paths.SetRoot(root)
	t.Cleanup(func() { paths.SetRoot("") })

	voices := filepath.Join(root, "data", "models", "omnivoice", "voices")
	if err := os.MkdirAll(voices, 0o755); err != nil {
		t.Fatal(err)
	}
	recording := filepath.Join(voices, "mikki.wav")
	write := func(content string, at time.Time) {
		t.Helper()
		if err := os.WriteFile(recording, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(recording, at, at); err != nil {
			t.Fatal(err)
		}
	}
	options := Options{Engine: EngineOmni, Voice: "mikki"}

	write("the first recording", time.Unix(1_000, 0))
	before := cacheKey("Halo", options)
	if again := cacheKey("Halo", options); again != before {
		t.Fatal("the same voice gives two different cache keys, so nothing is ever reused")
	}

	write("the recording that replaced it", time.Unix(2_000, 0))
	if after := cacheKey("Halo", options); after == before {
		t.Error("a replaced recording still finds the speech rendered in the old one")
	}
}

// The dropdown always offers the model's own invented voice, because it is the
// only thing that works before anybody has recorded anything -- and an empty
// list reads as an engine that is broken rather than one with no voice yet.
func TestOmniVoicesAlwaysOffersTheInventedVoice(t *testing.T) {
	voices := OmniVoices()
	if len(voices) == 0 {
		t.Fatal("OmniVoices() is empty")
	}
	if voices[0].Name != "" {
		t.Errorf("the first offer is %q, want the unnamed invented voice", voices[0].Name)
	}
}
