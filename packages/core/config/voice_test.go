package config

import (
	"os"
	"path/filepath"
	"testing"
)

// Upgrading moves a machine onto her voice once, and after that never changes
// which voice reads. These go through Load rather than calling the migrations
// directly, because reading a real file is the thing that actually happens to
// somebody upgrading.

func load(t *testing.T, contents string) Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	settings, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return settings
}

// The default: OmniVoice, reading in the voice MikkiLens ships with.
func wantDefaultVoice(t *testing.T, settings Config) {
	t.Helper()
	if settings.Speech.Engine != "omnivoice" {
		t.Errorf("engine = %q, want omnivoice", settings.Speech.Engine)
	}
	if settings.Speech.Voice != "mikkiru" {
		t.Errorf("voice = %q, want mikkiru", settings.Speech.Voice)
	}
}

// The case that matters: a machine that has been used, upgraded. The settings
// page saves the engine with everything else, so it is written down whether or
// not anybody chose it -- and it was still left reading in Supertonic, with
// OmniVoice never downloaded.
func TestASavedFileIsMovedOntoHerVoice(t *testing.T) {
	settings := load(t, `
[speech]
engine = 'local'
voice = 'F1'
rate = '+0%'
`)
	wantDefaultVoice(t, settings)
	if settings.Speech.DefaultVoiceGiven != "mikkiru" {
		t.Errorf("default_voice_given = %q, want it recorded", settings.Speech.DefaultVoiceGiven)
	}
}

// A chat or donation voice from another engine would read in a voice OmniVoice
// invents, so it goes back to following the main voice. One of her own
// recordings is an OmniVoice voice already, and stays.
func TestOtherEnginesChatVoicesFollowHerVoice(t *testing.T) {
	settings := load(t, `
[speech]
engine = 'online'
voice = 'id-ID-GadisNeural'
chat_voice = 'id-ID-ArdiNeural'
donation_voice = 'mikki'
`)
	wantDefaultVoice(t, settings)
	if settings.Speech.ChatVoice != "" {
		t.Errorf("chat_voice = %q, want it following her voice", settings.Speech.ChatVoice)
	}
	if settings.Speech.DonationVoice != "mikki" {
		t.Errorf("donation_voice = %q, want her recording kept", settings.Speech.DonationVoice)
	}
}

// Once, and only once: a voice she picks afterwards survives being saved and
// read back.
func TestAVoiceChosenAfterwardsIsKept(t *testing.T) {
	settings := load(t, `
[speech]
engine = 'local'
voice = 'F1'
`)
	settings.Speech.Engine = "online"
	settings.Speech.Voice = "id-ID-ArdiNeural"

	path := filepath.Join(t.TempDir(), "config.toml")
	if _, err := settings.Save(path); err != nil {
		t.Fatal(err)
	}
	again, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if again.Speech.Engine != "online" || again.Speech.Voice != "id-ID-ArdiNeural" {
		t.Errorf("speech = %q/%q, want her online/id-ID-ArdiNeural",
			again.Speech.Engine, again.Speech.Voice)
	}
}

// After that, a file with the engine taken out by hand still reads as the
// engine its voice belongs to.
func TestAnOnlineVoiceSurvivesTheUpgrade(t *testing.T) {
	settings := load(t, `
[speech]
default_voice_given = 'mikkiru'
voice = 'id-ID-GadisNeural'
rate = '+0%'
`)
	if settings.Speech.Engine != "online" {
		t.Errorf("engine = %q, want online: she picked that voice and should keep it",
			settings.Speech.Engine)
	}
	if settings.Speech.Voice != "id-ID-GadisNeural" {
		t.Errorf("voice = %q, want it untouched", settings.Speech.Voice)
	}
}

// Only the chat voice set is still somebody using the online voices.
func TestAnOnlineChatVoiceAloneAlsoSurvives(t *testing.T) {
	settings := load(t, `
[speech]
default_voice_given = 'mikkiru'
chat_voice = 'id-ID-ArdiNeural'
`)
	if settings.Speech.Engine != "online" {
		t.Errorf("engine = %q, want online", settings.Speech.Engine)
	}
}

func TestAnOnlineDonationVoiceAloneAlsoSurvives(t *testing.T) {
	settings := load(t, `
[speech]
default_voice_given = 'mikkiru'
donation_voice = 'en-US-AriaNeural'
`)
	if settings.Speech.Engine != "online" {
		t.Errorf("engine = %q, want online", settings.Speech.Engine)
	}
}

// Nothing was chosen, so there is nothing to preserve, and the new default
// takes over -- the voice as well as the engine. An empty voice here meant
// "whatever the engine starts with", not OmniVoice's invented voice.
func TestNoVoiceChosenTakesTheNewDefault(t *testing.T) {
	wantDefaultVoice(t, load(t, `
[speech]
voice = ''
rate = '+0%'
`))
}

func TestASpeechSectionWithNoVoiceAtAllTakesTheNewDefault(t *testing.T) {
	wantDefaultVoice(t, load(t, `
[speech]
rate = '+0%'
volume = 100
`))
}

// A file with no speech section at all, and a machine with no file at all.
func TestNoSpeechSectionTakesTheNewDefault(t *testing.T) {
	wantDefaultVoice(t, load(t, "[audio]\nsample_rate = 16000\n"))
}

func TestAFreshInstallGetsHerOwnVoice(t *testing.T) {
	wantDefaultVoice(t, Default())
}

// A value she has just chosen is never something to second-guess -- including
// choosing the local engine while an online voice name is still in the file,
// which is exactly what the settings page writes mid-switch.
func TestAnExplicitEngineIsNeverOverruled(t *testing.T) {
	settings := load(t, `
[speech]
default_voice_given = 'mikkiru'
engine = 'local'
voice = 'id-ID-GadisNeural'
`)
	if settings.Speech.Engine != "local" {
		t.Errorf("engine = %q, want the explicit local", settings.Speech.Engine)
	}

	settings = load(t, `
[speech]
default_voice_given = 'mikkiru'
engine = 'windows'
voice = 'id-ID-GadisNeural'
`)
	if settings.Speech.Engine != "windows" {
		t.Errorf("engine = %q, want the explicit windows", settings.Speech.Engine)
	}
}

// With the engine said outright, an empty voice is a choice too: for OmniVoice
// it is the model inventing a voice, and that is hers to pick.
func TestAnEmptyVoiceBesideAnExplicitEngineStaysEmpty(t *testing.T) {
	settings := load(t, `
[speech]
default_voice_given = 'mikkiru'
engine = 'omnivoice'
voice = ''
`)
	if settings.Speech.Voice != "" {
		t.Errorf("voice = %q, want the empty voice she chose", settings.Speech.Voice)
	}
}

// Likewise a Supertonic voice name with no engine beside it keeps the local
// voice.
func TestASupertonicVoiceKeepsTheLocalEngine(t *testing.T) {
	settings := load(t, `
[speech]
default_voice_given = 'mikkiru'
voice = 'F3'
`)
	if settings.Speech.Engine != "local" {
		t.Errorf("engine = %q, want local: she picked F3 and should keep it", settings.Speech.Engine)
	}
	if settings.Speech.Voice != "F3" {
		t.Errorf("voice = %q, want it untouched", settings.Speech.Voice)
	}
}
