package assets

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/exzork/mikkilens/packages/audio/tts/omnivoice"
)

// StageOmni is OmniVoice: a language model, a decoder, a tokenizer, and
// optionally the encoder that turns a recording into a voice. About two
// gigabytes.
//
// Unlike the local voice, this is never part of a first-run download. It is
// five times the size, it is only useful to somebody who has a recording to
// clone, and nothing falls back to it -- so fetching it because it exists
// would be two gigabytes spent on a guess. It is fetched when she chooses it,
// and not before. See MissingOmni.
const StageOmni Stage = "omnivoice"

// The export this is fetched from, pinned to a revision rather than a branch
// for the same reason the local voice is: a voice that changed under her
// between one stream and the next, with no way to see what happened, is the
// opposite of what this application is for.
//
// It is a community export rather than an official one, because k2-fsa publish
// OmniVoice as PyTorch weights and MikkiLens has no Python in it. That is worth
// knowing: it means the weights here were converted by somebody outside the
// project that trained them. They are checked by size on arrival, and by the
// only test that really settles it -- whether what comes out is the sentence
// that went in.
const (
	omniRepository = "https://huggingface.co/lanmower/OmniVoice-ONNX/resolve"
	omniRevision   = "main"
)

func omniURL(name string) string {
	return fmt.Sprintf("%s/%s/%s", omniRepository, omniRevision, name)
}

// omniFiles are the models, in the order they are fetched: the tokenizer and
// the graph structures first, then the weights smallest to largest.
//
// Smallest first, as with the local voice, because the common interruption is
// a connection dropping partway. Fetched the other way round, an interruption
// leaves 1.2 GB of language model on disk with no tokenizer to drive it, which
// reads as a voice that will not load; this way, what is missing is always the
// thing that was still coming.
//
// The encoder is last of all and is the one piece that is not needed to speak.
// Somebody whose connection drops after the decoder still has a working voice;
// what they do not have yet is the ability to add a new one from a recording.
var omniFiles = []struct {
	name string
	into string
}{
	{"tokenizer.json", ""},
	{"omnivoice_lm.onnx", "onnx"},
	{"audio_decoder.onnx", "onnx"},
	{"audio_encoder.onnx", "onnx"},
	{"audio_decoder.onnx.data", "onnx"},
	{"omnivoice_lm.onnx.data", "onnx"},
	{"audio_encoder.onnx.data", "onnx"},
}

// MissingOmni is what OmniVoice still needs.
//
// Its own function rather than part of [Missing] for the same reason
// MissingVoice is: it answers a different question from a different setting,
// and nobody reading through the Edge voices should be offered two gigabytes.
func MissingOmni(engine string) Wanted {
	if engine != "omnivoice" {
		return Wanted{}
	}
	if omnivoice.Installed() && omnivoice.EncoderInstalled() {
		return Wanted{}
	}
	return Wanted{Stages: []Stage{StageOmni}, Bytes: Bytes[StageOmni]}
}

// WithOmni puts OmniVoice on the end of a download.
//
// The end, flatly, with no rule about what it goes in front of -- unlike
// WithVoice and WithMusic, which both have opinions about the graphics build.
// Nothing falls back to OmniVoice, so nothing is waiting on it, and it is
// larger than everything else in the list put together. Anything it went in
// front of would be made to wait twenty minutes for no reason.
func WithOmni(wanted, omni Wanted) Wanted {
	if omni.Empty() {
		return wanted
	}
	return Wanted{
		Stages: append(append([]Stage{}, wanted.Stages...), omni.Stages...),
		Bytes:  wanted.Bytes + omni.Bytes,
	}
}

// fetchOmni gets the models and the tokenizer.
//
// The runtime comes first when it is not already here, for the same reason the
// local voice fetches it first: two gigabytes of models with nothing able to
// run them is a worse place to stop than not having started.
func (i *Installer) fetchOmni(ctx context.Context, track func(int64, int64, float64)) error {
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

	for _, file := range omniFiles {
		target := filepath.Join(omnivoice.Dir(), file.into, file.name)
		if exists(target) {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := download(ctx, omniURL(file.name), target, 0, track); err != nil {
			return err
		}
	}

	// The folder a recording goes in. Made here rather than on first use so
	// that somebody who has just downloaded this and gone looking for where to
	// put a wav file finds the place already waiting for them.
	return os.MkdirAll(filepath.Join(omnivoice.Dir(), "voices"), 0o755)
}
