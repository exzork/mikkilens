package engine

import (
	"strings"
	"testing"

	"github.com/exzork/mikkilens/packages/core/i18n"
	"github.com/exzork/mikkilens/packages/core/intent"
)

// The one thing MikkiLens says to the audience rather than to her. It is read
// out live, to people who have just arrived, so what it says is worth pinning
// in both languages rather than left to whatever a locale file drifts into.

func TestTheIntroductionIsRightInBothLanguages(t *testing.T) {
	for language, want := range map[string]string{
		"id": "Halo semuanya, aku MikkiLens, siap membantu!",
		"en": "Hello everyone, I'm MikkiLens, here to help!",
	} {
		if spoken := i18n.Load(language).T("hello.intro"); spoken != want {
			t.Errorf("%s says %q, want %q", language, spoken, want)
		}
	}
}

// A missing key reads out as the key itself -- "hello.intro" spoken aloud --
// which is the failure worth catching here rather than on stream.
func TestTheIntroductionIsNotAMissingKey(t *testing.T) {
	for _, language := range []string{"id", "en"} {
		spoken := i18n.Load(language).T("hello.intro")
		if strings.Contains(spoken, "hello.intro") {
			t.Errorf("%s has no hello.intro; it would read the key aloud: %q", language, spoken)
		}
		if strings.TrimSpace(spoken) == "" {
			t.Errorf("%s introduces her with nothing at all", language)
		}
	}
}

// She says her own name in it, and her name is the wake word. That is only
// safe because the gate closes for sentences that mention it -- otherwise
// MikkiLens would introduce herself and then answer herself. Pinned here
// because the two live in different files and nothing else connects them.
func TestIntroducingHerselfDoesNotWakeHer(t *testing.T) {
	for _, language := range []string{"id", "en"} {
		intro := i18n.Load(language).T("hello.intro")
		if !mentionsWakeWord(intro, "mikkilens") {
			t.Errorf("%s: the introduction no longer says her name, so the wake word "+
				"is left open while it is read: %q", language, intro)
		}
	}
}

// A command in code and nowhere she can say it is not a command.
func TestBothCommandFilesCanAskForAnIntroduction(t *testing.T) {
	for _, path := range []string{"../../commands.id.toml", "../../commands.en.toml"} {
		set, err := intent.SetFromFile(path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		command, ok := set.Commands["say_hello"]
		if !ok {
			t.Errorf("%s has no say_hello command", path)
			continue
		}
		if len(command.Phrases) == 0 {
			t.Errorf("%s: say_hello has no phrases", path)
		}

		// Saying hello takes nothing, so nothing should be declared. A slot
		// here would have the model inventing a value to fill it.
		option := optionFor(t, set, "say_hello")
		if len(option.Slots) != 0 || len(option.Required) != 0 {
			t.Errorf("%s: saying hello takes no arguments, got %v", path, option.Slots)
		}
		for _, phrase := range option.Phrases {
			if strings.TrimSpace(phrase) == "" {
				t.Errorf("%s: an empty phrase would describe nothing", path)
			}
		}
	}
}

// Asked for and never registered is the same as not existing, and the
// registration is one line in another file.
func TestSayingHelloIsHandled(t *testing.T) {
	handlers := helloHandlers(&Engine{})
	if _, ok := handlers["say_hello"]; !ok {
		t.Fatalf("say_hello has no handler; got %v", handlers)
	}
}

// The phrase she actually asked for. Everything else here is a variant, but
// this one came from her, and it must keep working whatever else is tuned.
func TestTheIndonesianPhraseSheAskedForIsThere(t *testing.T) {
	set, err := intent.SetFromFile("../../commands.id.toml")
	if err != nil {
		t.Fatal(err)
	}
	// Read back the way the matcher sees them, which is the only reading that
	// decides whether saying it out loud works.
	phrases := optionFor(t, set, "say_hello").Phrases
	for _, phrase := range phrases {
		if strings.EqualFold(strings.TrimSpace(phrase), "katakan halo") {
			return
		}
	}
	t.Errorf("\"katakan halo\" is not one of the phrases: %v", phrases)
}
