package config

import (
	"os"
	"path/filepath"
	"testing"
)

// Upgrading must not quietly change which voice reads. These go through Load
// rather than calling the migration directly, because reading a real file is
// the thing that actually happens to somebody upgrading.

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

// The case that matters: a machine running 0.10 today, upgraded.
func TestAnOnlineVoiceSurvivesTheUpgrade(t *testing.T) {
	settings := load(t, `
[speech]
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
chat_voice = 'id-ID-ArdiNeural'
`)
	if settings.Speech.Engine != "online" {
		t.Errorf("engine = %q, want online", settings.Speech.Engine)
	}
}

func TestAnOnlineDonationVoiceAloneAlsoSurvives(t *testing.T) {
	settings := load(t, `
[speech]
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
engine = 'local'
voice = 'id-ID-GadisNeural'
`)
	if settings.Speech.Engine != "local" {
		t.Errorf("engine = %q, want the explicit local", settings.Speech.Engine)
	}

	settings = load(t, `
[speech]
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
engine = 'omnivoice'
voice = ''
`)
	if settings.Speech.Voice != "" {
		t.Errorf("voice = %q, want the empty voice she chose", settings.Speech.Voice)
	}
}

// A Supertonic voice name, from somebody who chose it while the local voice was
// the default and never wrote the engine down, keeps the local voice.
func TestASupertonicVoiceKeepsTheLocalEngine(t *testing.T) {
	settings := load(t, `
[speech]
voice = 'F3'
`)
	if settings.Speech.Engine != "local" {
		t.Errorf("engine = %q, want local: she picked F3 and should keep it", settings.Speech.Engine)
	}
	if settings.Speech.Voice != "F3" {
		t.Errorf("voice = %q, want it untouched", settings.Speech.Voice)
	}
}
