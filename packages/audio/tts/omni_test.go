package tts

import "testing"

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
