package assets

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/exzork/mikkilens/packages/audio/tts/supertonic"
)

// StageVoice is the local voice: four networks and ten voice styles, about
// four hundred megabytes.
//
// It is fetched at first launch rather than the first time she speaks, for the
// same reason the music programs are. Lazily, the first thing MikkiLens ever
// said would begin with a four hundred megabyte download -- and unlike a song,
// what is waiting on it is the sentence explaining that something is being
// downloaded.
//
// It is not shipped in the installer because it is four times the size of the
// installer, and because somebody using the online voices should not be made
// to carry it.
const StageVoice Stage = "voice"

// voiceHost is the model repository. Pinned to a revision rather than main,
// for the same reason the whisper release is: a voice that changed under her
// between one stream and the next, with no way to see what happened, is the
// opposite of what this application is for.
const (
	voiceRepository = "https://huggingface.co/Supertone/supertonic-3/resolve"
	voiceRevision   = "3cadd1ee6394adea1bd021217a0e650ede09a323"
)

func voiceURL(name string) string {
	return fmt.Sprintf("%s/%s/%s", voiceRepository, voiceRevision, name)
}

// voiceFiles are everything the local voice needs, in the order they are
// fetched: the small tables first, then the models smallest to largest.
//
// Smallest first because the common interruption is a connection dropping
// partway. Fetched the other way round, an interruption leaves two hundred and
// fifty megabytes of estimator on disk and no character table to use it with,
// which reads as a voice that will not load; this way, what is missing is
// always the thing that was still coming.
var voiceFiles = []string{
	"onnx/tts.json",
	"onnx/unicode_indexer.json",
	"onnx/duration_predictor.onnx",
	"onnx/text_encoder.onnx",
	"onnx/vocoder.onnx",
	"onnx/vector_estimator.onnx",
}

// MissingVoice is what the local voice still needs.
//
// Its own function rather than part of [Missing] because it answers a
// different question from a different setting: this is wanted when the voice
// engine is the local one, and wanted not at all by somebody reading through
// the online voices. [WithVoice] is how it joins the first-run download.
func MissingVoice(engine string) Wanted {
	// Empty means local, the same as it does everywhere else.
	if engine != "" && engine != "local" {
		return Wanted{}
	}
	if supertonic.Installed() {
		return Wanted{}
	}
	return Wanted{Stages: []Stage{StageVoice}, Bytes: Bytes[StageVoice]}
}

// WithVoice folds the local voice into a first-run download.
//
// Ahead of the graphics build, behind everything else. The graphics build is
// an upgrade to recognition that already works, so it should not hold up a
// voice that does not exist yet; the speech model is the thing that lets her
// be heard at all, so nothing goes in front of that.
func WithVoice(wanted, voice Wanted) Wanted {
	if voice.Empty() {
		return wanted
	}

	combined := Wanted{Bytes: wanted.Bytes + voice.Bytes}
	for _, stage := range wanted.Stages {
		if stage == StageGPU && voice.Stages != nil {
			combined.Stages = append(combined.Stages, voice.Stages...)
			voice.Stages = nil
		}
		combined.Stages = append(combined.Stages, stage)
	}
	combined.Stages = append(combined.Stages, voice.Stages...)
	return combined
}

// fetchVoice gets the models, the character table and the voice styles.
//
// The runtime comes first when it is not already here. The wake word usually
// fetches it, but the wake word can be switched off, and four hundred
// megabytes of models with nothing able to run them is a worse place to stop
// than not having started.
func (i *Installer) fetchVoice(ctx context.Context, track func(int64, int64, float64)) error {
	if !exists(filepath.Join(modelsDir(), runtimeLibrary())) {
		if runtime.GOOS != "windows" {
			return &Error{Reason: "the voice runtime is only fetched " +
				"automatically on Windows; put libonnxruntime.so in data/models"}
		}
		url := fmt.Sprintf(
			"https://github.com/microsoft/onnxruntime/releases/download/v%s/onnxruntime-win-x64-%s.zip",
			onnxRuntime, onnxRuntime)
		if err := i.fetchArchive(ctx, url, "onnxruntime.zip",
			modelsDir(), wantedFromRuntime, track); err != nil {
			return err
		}
	}

	for _, name := range voiceFiles {
		target := filepath.Join(supertonic.Dir(), filepath.FromSlash(name))
		if exists(target) {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := download(ctx, voiceURL(name), target, 0, track); err != nil {
			return err
		}
	}

	// The voices last. They are three hundred kilobytes each against four
	// hundred megabytes of model, so they cost nothing to fetch and everything
	// to be without: the models load and then have nothing to speak in.
	for _, name := range supertonic.PresetVoices {
		target := filepath.Join(supertonic.Dir(), "voice_styles", name+".json")
		if exists(target) {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := download(ctx, voiceURL("voice_styles/"+name+".json"), target, 0, track); err != nil {
			return err
		}
	}
	return nil
}
