package stt

import (
	"testing"

	"github.com/exzork/mikkilens/packages/core/config"
)

// The config package spells the default out rather than importing this one;
// this keeps the two names the same.
func TestTheDefaultModelIsTurbo(t *testing.T) {
	if got := config.Default().STT.ModelSize; got != DefaultModel {
		t.Errorf("config default %q, stt default %q", got, DefaultModel)
	}
}

// Turbo is fetched quantised, and the file fetched has to be one findModel
// accepts as turbo -- otherwise it downloads, is not found, and downloads again
// at every start.
func TestTheDownloadedTurboFileIsRecognisedAsTurbo(t *testing.T) {
	file := ModelFile("large-v3-turbo")
	for _, suffix := range ModelSuffixes {
		if file == "ggml-large-v3-turbo"+suffix {
			return
		}
	}
	t.Errorf("%s is not among the names accepted for turbo", file)
}
