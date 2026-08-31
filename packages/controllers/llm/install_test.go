package llm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The catalogue is a set of promises about what she is downloading: how big it
// is, and whether it can do the thing she is downloading it for. Getting
// either wrong costs her gigabytes and an hour.

func TestEveryModelIsFetchableAndDescribed(t *testing.T) {
	for _, model := range Models {
		if model.Name == "" || model.File == "" || model.URL == "" {
			t.Errorf("%+v is missing something it needs to be downloaded", model)
		}
		if !strings.HasSuffix(model.File, ".gguf") {
			t.Errorf("%s is not a gguf", model.File)
		}
		if !strings.HasPrefix(model.URL, "https://") {
			t.Errorf("%s is not fetched over https", model.Name)
		}
		if model.Bytes <= 0 {
			t.Errorf("%s has no size, so she cannot be told what she is agreeing to",
				model.Name)
		}
		if model.Summary == "" {
			t.Errorf("%s has nothing said about it", model.Name)
		}
	}
}

// A projector is a separate file, and half of one is useless. If a model
// claims vision it must carry everything needed for it.
func TestAVisionModelCarriesItsWholeProjector(t *testing.T) {
	for _, model := range Models {
		if !model.Vision() {
			if model.ProjectorFile != "" || model.ProjectorBytes != 0 {
				t.Errorf("%s has half a projector", model.Name)
			}
			continue
		}
		if model.ProjectorFile == "" || model.ProjectorBytes <= 0 {
			t.Errorf("%s claims vision without a complete projector", model.Name)
		}
		if !strings.HasPrefix(model.ProjectorURL, "https://") {
			t.Errorf("%s fetches its projector over %q", model.Name, model.ProjectorURL)
		}
	}
}

// She is quoted a size before agreeing, and it has to include the projector --
// otherwise a "2.5 GB" download quietly turns into 3.3 GB.
func TestTheQuotedSizeIncludesTheProjector(t *testing.T) {
	for _, model := range Models {
		if model.TotalBytes() < model.Bytes {
			t.Errorf("%s quotes less than the model itself", model.Name)
		}
		if model.Vision() && model.TotalBytes() == model.Bytes {
			t.Errorf("%s quotes %d bytes but also downloads a projector",
				model.Name, model.Bytes)
		}
	}
}

// At least one model must be able to see, or the vision fallback can never
// happen and the feature is decoration.
func TestSomethingInTheCatalogueCanSee(t *testing.T) {
	for _, model := range Models {
		if model.Vision() {
			return
		}
	}
	t.Error("no model offers vision, so screen description can never work locally")
}

func TestModelsAreFoundByName(t *testing.T) {
	first := Models[0].Name
	if _, ok := ModelByName(first); !ok {
		t.Errorf("%s must be selectable by name", first)
	}
	if _, ok := ModelByName(strings.ToUpper(first)); !ok {
		t.Error("names must not be case sensitive; she may have typed it")
	}
	if _, ok := ModelByName("something-else"); ok {
		t.Error("an unknown name must not resolve")
	}
}

// The projector is only usable if it is actually on disk next to the model. A
// model whose projector never finished downloading must run as text only
// rather than being started with a missing file.
func TestAProjectorIsOnlyUsedWhenItIsReallyThere(t *testing.T) {
	directory := t.TempDir()

	var vision Model
	for _, model := range Models {
		if model.Vision() {
			vision = model
			break
		}
	}
	modelPath := filepath.Join(directory, vision.File)
	if err := os.WriteFile(modelPath, []byte("gguf"), 0o644); err != nil {
		t.Fatal(err)
	}

	if found := ProjectorFor(modelPath); found != "" {
		t.Errorf("found a projector at %q that does not exist", found)
	}

	projector := filepath.Join(directory, vision.ProjectorFile)
	if err := os.WriteFile(projector, []byte("mmproj"), 0o644); err != nil {
		t.Fatal(err)
	}
	if found := ProjectorFor(modelPath); found != projector {
		t.Errorf("projector resolved to %q, want %q", found, projector)
	}
}

// A model MikkiLens does not know must never be handed a projector belonging
// to a different one. Loading a server with a mismatched projector would fail
// in a way that looks like the model itself is broken.
func TestAnUnknownModelGetsNoProjector(t *testing.T) {
	directory := t.TempDir()

	stranger := filepath.Join(directory, "some-other-model.gguf")
	if err := os.WriteFile(stranger, []byte("gguf"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Put a real projector right beside it.
	for _, model := range Models {
		if model.Vision() {
			_ = os.WriteFile(filepath.Join(directory, model.ProjectorFile),
				[]byte("mmproj"), 0o644)
		}
	}

	if found := ProjectorFor(stranger); found != "" {
		t.Errorf("gave an unknown model the projector at %q", found)
	}
}
