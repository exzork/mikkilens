package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/exzork/mikkilens/packages/core/config"
)

// Volumes are one shape now -- a whole percentage from 0 to 100 -- and two
// older shapes have to keep working, because the config file on her machine
// was written by whatever version she had before this one.

func loadTOML(t *testing.T, body string) config.Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	settings, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return settings
}

// The speech volumes used to be what the online voice was asked for: "-40%"
// meant forty percent below the voice's own level, which is sixty percent of
// it. The tones and the music were fractions of one. Upgrading must not change
// how loud she is -- she would have to find every one of them again by ear.
func TestVolumesWrittenTheOldWayAreCarriedOver(t *testing.T) {
	settings := loadTOML(t, `
[speech]
volume = '-40%'
chat_volume = '-45%'
donation_volume = '+0%'
earcon_volume = 0.2

[music]
volume = 0.7
duck_volume = 0.25
`)

	for _, test := range []struct {
		what string
		got  int
		want int
	}{
		{"speech volume", settings.Speech.Volume, 60},
		{"chat volume", settings.Speech.ChatVolume, 55},
		{"donation volume", settings.Speech.DonationVolume, 100},
		{"tone volume", settings.Speech.EarconVolume, 20},
		{"music volume", settings.Music.Volume, 70},
		{"duck volume", settings.Music.DuckVolume, 25},
	} {
		if test.got != test.want {
			t.Errorf("%s = %d, want %d", test.what, test.got, test.want)
		}
	}
}

// An empty chat volume used to mean "as loud as the voice". Kept rather than
// defaulted, because it is what she has been hearing.
func TestAnEmptyChatVolumeBecomesTheSpeechVolume(t *testing.T) {
	settings := loadTOML(t, "[speech]\nvolume = '-20%'\nchat_volume = ''\n")
	if settings.Speech.ChatVolume != 80 {
		t.Errorf("chat volume = %d, want the speech volume 80", settings.Speech.ChatVolume)
	}
}

// The settings page speaks JSON, where every number is a float and go-toml
// will not put one in an int field. Sixty percent has to survive that, and one
// percent has to stay one percent rather than being read as the fraction the
// tones used to be written as.
func TestAVolumeFromTheSettingsPageArrivesAsAWholePercent(t *testing.T) {
	settings := config.FromMap(map[string]any{
		"speech": map[string]any{"volume": 60.0, "earcon_volume": 1.0},
	})
	if settings.Speech.Volume != 60 {
		t.Errorf("speech volume = %d, want 60", settings.Speech.Volume)
	}
	if settings.Speech.EarconVolume != 1 {
		t.Errorf("tone volume = %d, want 1", settings.Speech.EarconVolume)
	}
}

// Nothing outside 0 to 100 can be played, so nothing outside it is kept.
func TestAVolumeIsHeldInsideItsRange(t *testing.T) {
	settings := config.FromMap(map[string]any{
		"speech": map[string]any{"volume": 150, "chat_volume": -5},
	})
	if settings.Speech.Volume != 100 || settings.Speech.ChatVolume != 0 {
		t.Errorf("volumes clamped to %d and %d, want 100 and 0",
			settings.Speech.Volume, settings.Speech.ChatVolume)
	}
}

// One unreadable volume costs its own default, not the whole file. Refusing to
// start over a typo would leave her with a silent machine and no way to see
// why.
func TestAVolumeThatCannotBeReadKeepsItsDefault(t *testing.T) {
	settings := loadTOML(t, "[speech]\nvolume = 'loud'\nrate = '+25%'\n")
	if settings.Speech.Volume != config.Default().Speech.Volume {
		t.Errorf("speech volume = %d, want the default", settings.Speech.Volume)
	}
	if settings.Speech.Rate != "+25%" {
		t.Errorf("the rest of the section was lost: rate = %q", settings.Speech.Rate)
	}
}
