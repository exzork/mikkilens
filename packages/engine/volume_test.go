package engine

import (
	"testing"

	"github.com/exzork/mikkilens/packages/core/intent"
)

// Saying a volume out loud.
//
// What actually arrives is whatever recognition made of it: a bare number on
// one machine, words on another, words with the command's own tail still
// attached on a third. All of them have to land on the same percentage,
// because there is no screen to check it against.

func TestAVolumeCanBeSaidAsDigitsOrAsWords(t *testing.T) {
	indonesian := withLocale("id")
	for spoken, want := range map[string]int{
		"50":                   50,
		"50%":                  50,
		"50 persen":            50,
		"lima puluh persen":    50,
		"lima puluh":           50,
		"tujuh puluh lima":     75,
		"sembilan puluh lima":  95,
		"delapan belas persen": 18,
		"sepuluh":              10,
		"seratus":              100,
		"seratus persen":       100,
		"nol":                  0,
		"nol persen":           0,
		"jadi enam puluh":      60,
		"setengah":             50,
	} {
		percent, ok := indonesian.spokenPercent(spoken)
		if !ok || percent != want {
			t.Errorf("%q read as (%d, %v), want %d", spoken, percent, ok, want)
		}
	}

	english := withLocale("en")
	for spoken, want := range map[string]int{
		"fifty percent": 50,
		"fifty":         50,
		"seventy five":  75,
		"one hundred":   100,
		"a hundred":     100,
		"eighteen":      18,
		"zero":          0,
		"twenty":        20,
		"ninety nine":   99,
		"35":            35,
		"full":          100,
	} {
		percent, ok := english.spokenPercent(spoken)
		if !ok || percent != want {
			t.Errorf("%q read as (%d, %v), want %d", spoken, percent, ok, want)
		}
	}
}

// Nothing recognisable, and anything louder than everything, are refused
// rather than guessed at. A volume over full does not exist, so hearing one
// means a word was misheard -- and turning her up to full because "dua ratus"
// was heard for "seratus" is the kind of surprise she cannot see coming.
func TestAnUnreadableVolumeIsRefused(t *testing.T) {
	indonesian := withLocale("id")
	for _, spoken := range []string{"", "   ", "monokrom", "dua ratus", "seratus lima puluh", "150"} {
		if percent, ok := indonesian.spokenPercent(spoken); ok {
			t.Errorf("%q was read as %d percent, want refused", spoken, percent)
		}
	}

	english := withLocale("en")
	for _, spoken := range []string{"", "banana", "two hundred", "150"} {
		if percent, ok := english.spokenPercent(spoken); ok {
			t.Errorf("%q was read as %d percent, want refused", spoken, percent)
		}
	}
}

// A command that exists in code and in no command file is a command she cannot
// say, and one in the file with no handler is one that answers "not available
// yet". Both shipped files have to carry all four.
func TestBothCommandFilesCarryTheVolumeCommands(t *testing.T) {
	handlers := volumeHandlers(&Engine{})
	for _, path := range []string{"../../commands.id.toml", "../../commands.en.toml"} {
		set, err := intent.SetFromFile(path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		for id := range handlers {
			if !set.Has(id) {
				t.Errorf("%s has no %s command", path, id)
			}
		}
	}
}
