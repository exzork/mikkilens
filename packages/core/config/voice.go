package config

import "strings"

// ownVoiceEngine and ownVoice are the default: OmniVoice, reading in the voice
// that ships inside MikkiLens. Spelled out rather than taken from tts.EngineOmni
// and omnivoice.DefaultVoice, because this package sits under the audio
// packages, not above them; tts.TestTheDefaultIsHerOwnVoice keeps the names
// honest.
const (
	ownVoiceEngine = "omnivoice"
	ownVoice       = "mikkiru"
)

// migrateToOwnVoice moves a file written before her own voice shipped onto it,
// once.
//
// migrateVoiceEngine alone never did this on a machine that had been used. The
// settings page saves the whole speech section, engine included, so every file
// that has been saved even once says engine = 'local' or 'online' outright --
// and an engine written down is indistinguishable from an engine chosen. Her
// upgrade kept the old voice, and since the download follows the engine,
// OmniVoice's models never came down either.
//
// So a file that has not yet been given her voice is given it: the engine, the
// voice, and a chat or donation voice that names another engine's voice, which
// under OmniVoice would read in a voice the model invents. default_voice_given
// records that it happened. Anything she chooses after that is hers, and is
// never moved again.
//
// Only for a document read from disk, like migrateVolumes.
func migrateToOwnVoice(document map[string]any) {
	speech, ok := document["speech"].(map[string]any)
	if !ok {
		// No speech section: every value in it is already the default.
		return
	}
	if given, ok := speech["default_voice_given"].(string); ok && given == ownVoice {
		return
	}

	speech["engine"] = ownVoiceEngine
	speech["voice"] = ownVoice
	for _, key := range []string{"chat_voice", "donation_voice"} {
		if name, ok := speech[key].(string); ok && (strings.Contains(name, "-") || supertonicVoice(name)) {
			speech[key] = ""
		}
	}
	speech["default_voice_given"] = ownVoice
}

// migrateVoiceEngine keeps the voice she already picked.
//
// The engine setting did not exist before 0.11, and its default has changed
// since -- the local voice first, OmniVoice in her own voice now. Read plainly,
// a file that says nothing about an engine would start reading in whatever the
// default is today: the file still names a voice from some other engine, the
// new one has never heard of that name, and it falls back to its own. Somebody
// who settled on id-ID-ArdiNeural months ago would be met mid-stream by a
// stranger, with no announcement and nothing obviously wrong to go looking for.
//
// So a file that names a voice and says nothing about an engine is taken to
// mean the engine that name belongs to. She picked that voice; a new default is
// not a reason to overrule her.
//
// A file that names no voice at all takes the new default, voice and all.
// Nothing was chosen there, so there is nothing to preserve.
//
// Only for a document read from disk, like migrateVolumes, and after
// migrateToOwnVoice -- so by now it only meets a file that has already been
// given her voice and had its engine taken out by hand. The settings page sends
// the engine explicitly, and a value she has just chosen is never something to
// second-guess.
func migrateVoiceEngine(document map[string]any) {
	speech, ok := document["speech"].(map[string]any)
	if !ok {
		return
	}
	if engine, ok := speech["engine"].(string); ok && strings.TrimSpace(engine) != "" {
		return
	}

	// Every Edge voice is "language-REGION-NameNeural" and no other engine's
	// voice has a dash in it, so the shape of the string is enough to tell
	// which list a name was chosen from. The chat and donation voices count
	// too: either one set on its own is still somebody who was using them.
	voices := []string{"voice", "chat_voice", "donation_voice"}
	for _, key := range voices {
		if name, ok := speech[key].(string); ok && strings.Contains(name, "-") {
			speech["engine"] = "online"
			return
		}
	}
	for _, key := range voices {
		if name, ok := speech[key].(string); ok && supertonicVoice(name) {
			speech["engine"] = "local"
			return
		}
	}

	// An empty voice in a file like this meant "whatever the engine starts
	// with". Under OmniVoice an empty voice means something else -- no
	// reference at all, a voice the model makes up -- so the empty value is
	// dropped and the default voice arrives with the default engine.
	if name, ok := speech["voice"].(string); ok && strings.TrimSpace(name) == "" {
		delete(speech, "voice")
	}
}

// supertonicVoice reports whether a name is one of the ten Supertonic presets,
// F1 to F5 and M1 to M5.
func supertonicVoice(name string) bool {
	name = strings.TrimSpace(name)
	return len(name) == 2 && (name[0] == 'F' || name[0] == 'M') && name[1] >= '1' && name[1] <= '9'
}
