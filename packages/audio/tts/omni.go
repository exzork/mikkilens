package tts

import (
	"context"

	"github.com/exzork/mikkilens/packages/audio/tts/omnivoice"
)

// OmniVoice is the fourth engine, and the only one whose voices are not a
// list somebody picked from.
//
// The other three answer "which voice?" with a name from a fixed set -- F1,
// id-ID-GadisNeural, whatever Windows is set to. OmniVoice answers it with a
// recording. That difference runs all the way up through this file: what the
// settings page shows for this engine is the contents of a folder, and what
// puts something in that folder is somebody dropping a wav file there.
//
// See packages/audio/tts/omnivoice for what it costs and why it is slow.

// OmniInstalled reports whether OmniVoice has its models. The settings page
// asks, so choosing it can say what it will cost to download rather than
// silently doing nothing.
func OmniInstalled() bool { return omnivoice.Installed() }

// OmniReady reports whether the models are loaded and a phrase would be
// answered without first waiting for two gigabytes to open.
func OmniReady() bool { return omnivoice.Ready() }

// ReleaseOmni unloads OmniVoice and gives the memory back. Switching away from
// it calls this; see omnivoice.Release for why it matters on a machine that is
// also encoding video.
func ReleaseOmni() { omnivoice.Release() }

// OmniVoices lists what OmniVoice can speak in, for the dropdown.
//
// The empty-named first entry is the model choosing a voice for itself. It is
// offered rather than hidden because it is the only thing that works before
// anybody has recorded anything, and leaving the list empty would read as an
// engine that is broken rather than one that has not been given a voice yet.
func OmniVoices() []Voice {
	found := []Voice{{Name: "", Gender: "", Locale: ""}}
	for _, name := range omnivoice.Voices() {
		found = append(found, Voice{
			Name:   name,
			Gender: "",
			// OmniVoice reads six hundred languages in whatever voice it is
			// given, so a voice does not belong to a language and filing it
			// under one would hide it from every other.
			Locale: "",
		})
	}
	return found
}

// synthesizeOmni renders one phrase with OmniVoice.
//
// Rate arrives as the same "+15%" string every other engine takes, because it
// is one setting that has to mean the same thing whichever voice is reading.
// Here it becomes a divisor on the predicted length: the model fills exactly
// the time it is given, so asking for less time is the whole of asking it to
// speak faster.
func synthesizeOmni(ctx context.Context, text string, options Options) (Audio, error) {
	engine, err := omnivoice.Shared(ctx)
	if err != nil {
		return Audio{}, err
	}

	voice := options.Voice
	if !omnivoice.Known(voice) {
		// The configured voice is an Edge name, a Supertonic one, or a
		// recording that has been deleted. Reading in the model's own invented
		// voice is a much better answer than not reading.
		voice = ""
	}
	if voice != "" {
		// Preparing is a no-op once done. It is here rather than at startup
		// because a recording can appear at any time, and the first sentence
		// after dropping one in is the natural moment to pay for it.
		if err := omnivoice.Prepare(voice); err != nil {
			return Audio{}, err
		}
	}

	samples, err := engine.Speak(ctx, text, omnivoice.Options{
		Voice:    voice,
		Language: options.Language,
		Speed:    omniSpeed(options.Rate),
	})
	if err != nil {
		return Audio{}, err
	}
	return Audio{
		Samples:    samples,
		SampleRate: engine.SampleRate(),
		Channels:   1,
		Text:       text,
	}, nil
}

// omniSpeed turns "+15%" into the multiplier the length estimate wants.
//
// Unlike the local voice there is no model default to start from: OmniVoice
// has no speed of its own, only the length it is asked to fill, so "+0%" is
// exactly 1 and the arithmetic is the plain reading of the number.
func omniSpeed(rate string) float32 {
	return 1 + float32(parsePercent(rate))/100
}
