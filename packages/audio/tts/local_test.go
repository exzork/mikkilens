package tts

import (
	"fmt"
	"math"
	"slices"
	"testing"

	"github.com/exzork/mikkilens/packages/audio/tts/supertonic"
)

// A typo in a config file she edits by hand must not be the reason MikkiLens
// stops talking.
func TestAnUnknownEngineIsTheLocalOne(t *testing.T) {
	for _, name := range []string{"", "loc al", "Local", "edge", "supertonic"} {
		if got := resolveEngine(name); got != EngineLocal {
			t.Errorf("resolveEngine(%q) = %q, want %q", name, got, EngineLocal)
		}
	}
	if got := resolveEngine(EngineOnline); got != EngineOnline {
		t.Errorf("resolveEngine(%q) = %q", EngineOnline, got)
	}
	if got := resolveEngine(EngineWindows); got != EngineWindows {
		t.Errorf("resolveEngine(%q) = %q", EngineWindows, got)
	}
}

func TestEveryEngineTriesItselfFirst(t *testing.T) {
	for _, engine := range Engines {
		order := fallbackOrder(engine)
		if len(order) == 0 || order[0] != engine {
			t.Errorf("fallbackOrder(%q) = %v, want it to start with itself", engine, order)
		}
	}
}

// Choosing the plain Windows voice on purpose and being given a different one
// anyway is not a fallback; it is the setting being ignored.
func TestTheWindowsVoiceStandsAlone(t *testing.T) {
	if got := fallbackOrder(EngineWindows); len(got) != 1 {
		t.Errorf("fallbackOrder(windows) = %v, want only itself", got)
	}
}

// Whatever she picks, there is always something behind it, and Windows is
// always the last thing standing.
func TestNoChoiceCanEndInSilence(t *testing.T) {
	for _, engine := range []string{EngineLocal, EngineOnline} {
		order := fallbackOrder(engine)
		if len(order) < 2 {
			t.Errorf("fallbackOrder(%q) = %v, want something behind it", engine, order)
		}
		if order[len(order)-1] != EngineWindows {
			t.Errorf("fallbackOrder(%q) = %v, want Windows last", engine, order)
		}
	}
}

func TestAnEngineIsNeverTriedTwice(t *testing.T) {
	for _, engine := range Engines {
		order := fallbackOrder(engine)
		seen := map[string]bool{}
		for _, tried := range order {
			if seen[tried] {
				t.Errorf("fallbackOrder(%q) = %v, tries %q twice", engine, order, tried)
			}
			seen[tried] = true
		}
	}
}

// When the online voice stands in for a local one, the configured voice names
// something Edge has never heard of, and the language default has to take over.
func TestTheOnlineVoiceRefusesALocalName(t *testing.T) {
	got := onlineVoice(Options{Voice: "F1", OnlineVoice: "id-ID-GadisNeural"})
	if got != "id-ID-GadisNeural" {
		t.Errorf("onlineVoice(...) = %q, want the language default", got)
	}
}

func TestAnEdgeNameIsKeptAsItIs(t *testing.T) {
	got := onlineVoice(Options{Voice: "id-ID-ArdiNeural", OnlineVoice: "id-ID-GadisNeural"})
	if got != "id-ID-ArdiNeural" {
		t.Errorf("onlineVoice(...) = %q, want the configured voice", got)
	}
}

// With nothing configured at all there still has to be a name, because an
// empty one is refused by the service rather than defaulted by it.
func TestThereIsAlwaysSomeOnlineVoice(t *testing.T) {
	if got := onlineVoice(Options{}); got == "" {
		t.Error("onlineVoice(Options{}) is empty, which the service refuses")
	}
}

// "+0%" has to land on the model's own default of 1.05 rather than on 1.0:
// that is the speed its authors found sounds like ordinary speech, so "no
// change" means no change from that.
func TestNoRateChangeIsTheModelsOwnSpeed(t *testing.T) {
	for _, rate := range []string{"+0%", "", "nonsense"} {
		if got := localSpeed(rate); math.Abs(float64(got)-1.05) > 0.0001 {
			t.Errorf("localSpeed(%q) = %v, want 1.05", rate, got)
		}
	}
}

func TestAFasterRateIsAFasterVoice(t *testing.T) {
	slow, ordinary, fast := localSpeed("-25%"), localSpeed("+0%"), localSpeed("+35%")
	if !(slow < ordinary && ordinary < fast) {
		t.Errorf("localSpeed does not order: %v, %v, %v", slow, ordinary, fast)
	}
}

// The same words in the same voice mean different audio under a different
// engine, and a shared key would be heard as the setting having done nothing.
func TestTheCacheKeyTellsTheEnginesApart(t *testing.T) {
	local := cacheKey("Halo", Options{Engine: EngineLocal, Voice: "F1"})
	online := cacheKey("Halo", Options{Engine: EngineOnline, Voice: "F1"})
	if local == online {
		t.Error("the same key is used for two different engines")
	}
}

func TestTheCacheKeyTellsTheLanguagesApart(t *testing.T) {
	indonesian := cacheKey("Halo", Options{Language: "id"})
	english := cacheKey("Halo", Options{Language: "en"})
	if indonesian == english {
		t.Error("the same key is used for two different languages")
	}
}

// An empty engine and an explicit "local" are the same setting, so they must
// not each render and store their own copy of every phrase.
func TestTheDefaultEngineSharesItsCache(t *testing.T) {
	if cacheKey("Halo", Options{}) != cacheKey("Halo", Options{Engine: EngineLocal}) {
		t.Error("the default engine does not share a key with local")
	}
}

// The local voice hands over samples rather than bytes, so what goes in the
// disk cache is written here and read back by Decode. A round trip that loses
// the rate or the channel count is a cached phrase that plays at the wrong
// pitch.
func TestEncodedAudioSurvivesBeingReadBack(t *testing.T) {
	original := Audio{
		Samples:    []float32{0, 0.5, -0.5, 0.999, -0.999},
		SampleRate: 44100,
		Channels:   1,
	}

	decoded, err := Decode(EncodeWAV(original))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if decoded.SampleRate != original.SampleRate {
		t.Errorf("sample rate %d, want %d", decoded.SampleRate, original.SampleRate)
	}
	if decoded.Channels != original.Channels {
		t.Errorf("%d channels, want %d", decoded.Channels, original.Channels)
	}
	if len(decoded.Samples) != len(original.Samples) {
		t.Fatalf("%d samples, want %d", len(decoded.Samples), len(original.Samples))
	}
	for at, sample := range decoded.Samples {
		// 16-bit, so a sample comes back within one step of where it went in.
		if math.Abs(float64(sample-original.Samples[at])) > 1.0/32768 {
			t.Errorf("sample %d came back as %v, want %v", at, sample, original.Samples[at])
		}
	}
}

// A sample slightly over full scale is a rounding artefact. Wrapping it turns
// it into a click, which is the one thing worse than a quiet clip.
func TestTooLoudASampleIsClampedRatherThanWrapped(t *testing.T) {
	decoded, err := Decode(EncodeWAV(Audio{
		Samples:    []float32{2.5, -2.5},
		SampleRate: 44100,
		Channels:   1,
	}))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if decoded.Samples[0] < 0.9 {
		t.Errorf("a loud sample came back as %v, want it near full scale", decoded.Samples[0])
	}
	if decoded.Samples[1] > -0.9 {
		t.Errorf("a loud sample came back as %v, want it near full scale", decoded.Samples[1])
	}
}

// Stored speech is found again whichever engine wrote it, so the extension the
// online voice uses and the one the local voice uses both have to be swept up
// when the cache is thrown away.
func TestBothCacheExtensionsAreKnown(t *testing.T) {
	for _, extension := range []string{".mp3", ".wav"} {
		if !slices.Contains(cacheExtensions, extension) {
			t.Errorf("%s is written but never looked for or cleared", extension)
		}
	}
}

// Asking the local voice to go faster than it can does not produce fast
// speech, it produces the sentence with words missing -- at confident volume,
// sounding finished. So the rate is clamped, and these are the two ends of it.

func TestARateTooFastForTheModelIsClamped(t *testing.T) {
	ceiling := localSpeed(fmt.Sprintf("+%d%%", LocalSpeedCeiling()))
	if ceiling > supertonic.MaxSpeed+0.001 {
		t.Errorf("the ceiling rate maps to %v, past the model's %v",
			ceiling, supertonic.MaxSpeed)
	}
	// And just past it, the number she is told is the last one that works.
	beyond := localSpeed(fmt.Sprintf("+%d%%", LocalSpeedCeiling()+1))
	if beyond <= supertonic.MaxSpeed {
		t.Errorf("+%d%% maps to %v, which is still within the model's %v -- "+
			"the ceiling is being reported lower than it is",
			LocalSpeedCeiling()+1, beyond, supertonic.MaxSpeed)
	}
}

// The settings page offers up to +100%, because the online voice does go that
// fast. The local one has to survive being asked.
func TestTheTopOfTheSliderIsSurvivable(t *testing.T) {
	options := supertonic.Options{Speed: localSpeed("+100%")}
	if got := options.Speed; got <= supertonic.MaxSpeed {
		t.Skip("the slider no longer exceeds what the model can do")
	}
	// Clamping happens inside the engine; what matters here is that the number
	// handed to it is the honest one rather than pre-trimmed, so the engine
	// stays the single place that knows what the model can say.
	if localSpeed("+100%") <= localSpeed("+50%") {
		t.Error("the rate mapping is not monotonic")
	}
}

func TestTheSlowEndIsAlsoBounded(t *testing.T) {
	if supertonic.MinSpeed <= 0 || supertonic.MinSpeed >= supertonic.DefaultSpeed {
		t.Errorf("MinSpeed %v is not below the natural speed %v",
			supertonic.MinSpeed, supertonic.DefaultSpeed)
	}
}
