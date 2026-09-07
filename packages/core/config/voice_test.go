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
// takes over. This is how the local voice reaches anyone but a fresh install.
func TestNoVoiceChosenTakesTheNewDefault(t *testing.T) {
	settings := load(t, `
[speech]
voice = ''
rate = '+0%'
`)
	if settings.Speech.Engine != "local" {
		t.Errorf("engine = %q, want local", settings.Speech.Engine)
	}
}

func TestASpeechSectionWithNoVoiceAtAllTakesTheNewDefault(t *testing.T) {
	settings := load(t, `
[speech]
rate = '+0%'
volume = 100
`)
	if settings.Speech.Engine != "local" {
		t.Errorf("engine = %q, want local", settings.Speech.Engine)
	}
}

// A file with no speech section at all, and a machine with no file at all.
func TestNoSpeechSectionTakesTheNewDefault(t *testing.T) {
	settings := load(t, "[audio]\nsample_rate = 16000\n")
	if settings.Speech.Engine != "local" {
		t.Errorf("engine = %q, want local", settings.Speech.Engine)
	}
}

func TestAFreshInstallGetsTheLocalVoice(t *testing.T) {
	if Default().Speech.Engine != "local" {
		t.Errorf("engine = %q, want local", Default().Speech.Engine)
	}
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

// A local voice name is not a reason to switch to the online engine.
func TestALocalVoiceNameDoesNotLookOnline(t *testing.T) {
	settings := load(t, `
[speech]
voice = 'F3'
`)
	if settings.Speech.Engine != "local" {
		t.Errorf("engine = %q, want local", settings.Speech.Engine)
	}
	if settings.Speech.Voice != "F3" {
		t.Errorf("voice = %q, want it untouched", settings.Speech.Voice)
	}
}
