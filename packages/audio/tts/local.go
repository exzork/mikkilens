package tts

import (
	"context"

	"github.com/exzork/mikkilens/packages/audio/tts/supertonic"
)

// The three engines, named the way they are spoken about rather than after
// what they are made of. What she picks in settings is one of these.
const (
	// EngineLocal is Supertonic 3, running on this machine. It is the default:
	// it needs no network, no clock, and nobody else's permission.
	EngineLocal = "local"

	// EngineOnline is the Edge voices. Free, natural, and somebody else's
	// service to withdraw.
	EngineOnline = "online"

	// EngineWindows is the speech synthesizer built into Windows. It is the
	// floor rather than a choice: no download, no network, and it sounds like
	// it.
	EngineWindows = "windows"
)

// Engines are the choices, in the order the settings page offers them.
var Engines = []string{EngineLocal, EngineOnline, EngineWindows}

// resolveEngine settles on which voice to try first.
//
// An empty setting means local, and an unrecognised one means local too. This
// is read on every utterance from a file she can edit by hand, and a typo in
// it must not be the reason MikkiLens stops talking.
func resolveEngine(name string) string {
	switch name {
	case EngineOnline:
		return EngineOnline
	case EngineWindows:
		return EngineWindows
	default:
		return EngineLocal
	}
}

// LocalInstalled reports whether the local voice has its models. The settings
// page asks, so choosing it can say what it will cost to download rather than
// silently doing nothing.
func LocalInstalled() bool { return supertonic.Installed() }

// LocalVoices lists the installed local voices, for the dropdown.
func LocalVoices() []Voice {
	found := []Voice{}
	for _, voice := range supertonic.Voices() {
		found = append(found, Voice{
			Name:   voice.Name,
			Gender: voice.Gender,
			// The local voices are multilingual: one voice reads all
			// thirty-one languages, so there is no locale to file them under.
			// The dropdown shows them for every language, which is the truth.
			Locale: "",
		})
	}
	return found
}

// ReleaseLocal unloads the local voice and gives the memory back. Switching
// away from it calls this; see supertonic.Release for why it matters on a
// machine that is also encoding video.
func ReleaseLocal() { supertonic.Release() }

// LocalReady reports whether the local models are loaded and a phrase would be
// answered immediately rather than after a second of loading.
func LocalReady() bool { return supertonic.Ready() }

// synthesizeLocal renders one phrase with the local voice.
//
// Rate arrives as the same "+15%" string the online voice takes, because it is
// one setting that has to mean the same thing whichever voice is reading. Here
// it becomes a multiplier on the predicted duration.
func synthesizeLocal(ctx context.Context, text string, options Options) (Audio, error) {
	engine, err := supertonic.Shared(ctx)
	if err != nil {
		return Audio{}, err
	}

	voice := options.Voice
	if !supertonic.Known(voice) {
		// The configured voice is an Edge name, or a local voice that is no
		// longer installed. Reading in a different voice is a much better
		// answer than not reading.
		voice = supertonic.DefaultVoice
	}

	samples, err := engine.Speak(ctx, text, supertonic.Options{
		Voice:    voice,
		Language: options.Language,
		Speed:    localSpeed(options.Rate),
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

// localSpeed turns "+15%" into the multiplier the model wants.
//
// The model's own default is 1.05 rather than 1.0 -- that is what its authors
// found sounds like ordinary speech -- so a rate of "+0%" has to land there,
// not on 1.0. Her "+35%" for chat then means 35% faster than ordinary, which
// is what it means for the online voice too.
func localSpeed(rate string) float32 {
	return 1.05 * (1 + float32(parsePercent(rate))/100)
}
