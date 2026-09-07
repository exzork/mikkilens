package config

import "strings"

// migrateVoiceEngine keeps the voice she already picked.
//
// The engine setting did not exist before 0.11, and its default is the local
// voice. Read plainly, that means every machine upgrading would start reading
// in a voice nobody chose: the file still names an Edge voice, the local
// engine has never heard of that name, and it would fall back to its own
// default. Somebody who settled on id-ID-ArdiNeural months ago would be met
// mid-stream by a stranger, with no announcement and nothing obviously wrong
// to go looking for.
//
// So a file that names an online voice and says nothing about an engine is
// taken to mean the engine those names belong to. She picked that voice from
// the only list there was; the list growing is not a reason to overrule her.
//
// A file that names no voice at all is left alone and takes the new default.
// Nothing was chosen there, so there is nothing to preserve -- and if every
// existing machine were pinned to the online voices, the local one would
// reach nobody but a fresh install.
//
// Only for a document read from disk, like migrateVolumes. The settings page
// sends the engine explicitly, and a value she has just chosen is never
// something to second-guess.
func migrateVoiceEngine(document map[string]any) {
	speech, ok := document["speech"].(map[string]any)
	if !ok {
		return
	}
	if engine, ok := speech["engine"].(string); ok && strings.TrimSpace(engine) != "" {
		return
	}

	// Every Edge voice is "language-REGION-NameNeural" and no local voice has
	// a dash in it, so the shape of the string is enough to tell which list a
	// name was chosen from. The chat and donation voices count too: either one
	// set on its own is still somebody who was using the online voices.
	for _, key := range []string{"voice", "chat_voice", "donation_voice"} {
		if name, ok := speech[key].(string); ok && strings.Contains(name, "-") {
			speech["engine"] = "online"
			return
		}
	}
}
