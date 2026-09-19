package config

import "testing"

// Small was the default and is written into every file the settings page ever
// saved, so it cannot be told apart from a choice. It is moved to turbo once;
// anything chosen after that stays.

func TestASavedSmallModelIsMovedToTurbo(t *testing.T) {
	settings := load(t, `
[stt]
model_size = 'small'
`)
	if settings.STT.ModelSize != "large-v3-turbo" {
		t.Errorf("model_size = %q, want large-v3-turbo", settings.STT.ModelSize)
	}
	if settings.STT.DefaultModelGiven != "large-v3-turbo" {
		t.Errorf("default_model_given = %q, want it recorded", settings.STT.DefaultModelGiven)
	}
}

// Nobody ever had medium by default, so it was chosen.
func TestAChosenModelIsNotMoved(t *testing.T) {
	settings := load(t, `
[stt]
model_size = 'medium'
`)
	if settings.STT.ModelSize != "medium" {
		t.Errorf("model_size = %q, want medium kept", settings.STT.ModelSize)
	}
}

func TestSmallChosenAfterTheMoveIsKept(t *testing.T) {
	settings := load(t, `
[stt]
model_size = 'small'
default_model_given = 'large-v3-turbo'
`)
	if settings.STT.ModelSize != "small" {
		t.Errorf("model_size = %q, want small kept", settings.STT.ModelSize)
	}
}

func TestAFreshInstallGetsTurbo(t *testing.T) {
	if got := Default().STT.ModelSize; got != "large-v3-turbo" {
		t.Errorf("default model = %q, want large-v3-turbo", got)
	}
}
