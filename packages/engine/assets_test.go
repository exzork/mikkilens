package engine

import (
	"testing"

	"github.com/exzork/mikkilens/packages/audio/assets"
	"github.com/exzork/mikkilens/packages/core/config"
	"github.com/exzork/mikkilens/packages/core/paths"
)

// speechAssets is what decides whether saving the settings page starts a
// download. It is worth pinning because both ways of being wrong are quiet:
// wanting nothing leaves her with an engine that never arrives, and wanting
// something she already has starts two gigabytes for no reason.

// wantedFor points the project root at an empty directory, so that "is it
// installed?" answers no for everything and the question being tested is which
// stages the engine asks for rather than what this machine happens to have.
func wantedFor(t *testing.T, engine string) assets.Wanted {
	t.Helper()
	paths.SetRoot(t.TempDir())

	settings := config.Default()
	settings.Speech.Engine = engine
	return speechAssets(settings)
}

func TestChoosingOmniVoiceWithoutItsModelsAsksForThem(t *testing.T) {
	wanted := wantedFor(t, "omnivoice")

	if !wanted.Has(assets.StageOmni) {
		t.Errorf("stages = %v, want OmniVoice among them", wanted.Stages)
	}
	if wanted.Bytes <= 0 {
		t.Errorf("bytes = %d, want a size to say out loud before it starts", wanted.Bytes)
	}

	// The speech model is recognition's, not the voice's. Choosing how she
	// sounds must not start downloading how she hears.
	if wanted.Has(assets.StageModel) || wanted.Has(assets.StageWake) {
		t.Errorf("stages = %v, want nothing belonging to recognition", wanted.Stages)
	}
}

func TestChoosingTheLocalVoiceWithoutItsModelsAsksForThem(t *testing.T) {
	if wanted := wantedFor(t, "local"); !wanted.Has(assets.StageVoice) {
		t.Errorf("stages = %v, want the local voice among them", wanted.Stages)
	}
}

// The two engines with no models of their own. Saving with either chosen has
// nothing to fetch, and has to say so as an empty list rather than as a
// download of nothing -- the settings page puts a bar up for the difference.
func TestTheEnginesWithNoModelsAskForNothing(t *testing.T) {
	for _, engine := range []string{"windows", "online"} {
		t.Run(engine, func(t *testing.T) {
			if wanted := wantedFor(t, engine); !wanted.Empty() {
				t.Errorf("stages = %v, want nothing to fetch", wanted.Stages)
			}
		})
	}
}

// Choosing a recognition model that is not here downloads it, and only it:
// the engine and the graphics build are the same whichever model runs.
func TestChoosingAMissingRecognitionModelDownloadsIt(t *testing.T) {
	paths.SetRoot(t.TempDir())
	settings := config.Default()
	settings.STT.AutoInstall = true

	wanted := recognitionModel(settings)
	if len(wanted.Stages) != 1 || !wanted.Has(assets.StageModel) {
		t.Fatalf("stages = %v, want only the model", wanted.Stages)
	}
	if wanted.Bytes != assets.ModelBytes(settings.STT.ModelSize) {
		t.Errorf("bytes = %d, want the chosen model's size", wanted.Bytes)
	}
}
