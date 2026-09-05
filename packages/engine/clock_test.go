package engine

import (
	"strings"
	"testing"
	"time"

	"github.com/exzork/mikkilens/packages/core/i18n"
	"github.com/exzork/mikkilens/packages/core/intent"
)

// The clock is the one command that answers whether or not anything else is
// connected, so what it says has to be right in both languages and it has to
// reach the model like every other command.

func TestTheTimeIsSaidInTwentyFourHours(t *testing.T) {
	// Half past two in the afternoon, which is the hour that would be wrong if
	// this ever drifted to a twelve-hour clock.
	frozen := time.Date(2026, 9, 5, 14, 35, 0, 0, time.Local)
	formatted := frozen.Format("15:04")
	if formatted != "14:35" {
		t.Fatalf("formatted %q, want 14:35", formatted)
	}

	for language, want := range map[string]string{
		"id": "Sekarang jam 14:35.",
		"en": "It is 14:35.",
	} {
		spoken := i18n.Load(language).T("clock.now", i18n.Args{"time": formatted})
		if spoken != want {
			t.Errorf("%s says %q, want %q", language, spoken, want)
		}
	}
}

// now is a variable so a test is not at the mercy of when it runs. If it ever
// stops being one, the test above stops meaning anything.
func TestTheClockCanBeFrozen(t *testing.T) {
	original := now
	defer func() { now = original }()

	frozen := time.Date(2026, 9, 5, 9, 5, 0, 0, time.Local)
	now = func() time.Time { return frozen }
	if got := now().Format("15:04"); got != "09:05" {
		t.Errorf("frozen clock reads %q, want 09:05", got)
	}
}

// Every shipped command file must carry it, or the command exists in code and
// nowhere she can say it.
func TestBothCommandFilesCanAskForTheTime(t *testing.T) {
	for _, path := range []string{"../../commands.id.toml", "../../commands.en.toml"} {
		set, err := intent.SetFromFile(path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		command, ok := set.Commands["current_time"]
		if !ok {
			t.Errorf("%s has no current_time command", path)
			continue
		}
		if len(command.Phrases) == 0 {
			t.Errorf("%s: current_time has no phrases", path)
		}

		// The phrases become the tool's description, so an empty one would
		// leave the model choosing on the id alone.
		option := optionFor(t, set, "current_time")
		if len(option.Slots) != 0 || len(option.Required) != 0 {
			t.Errorf("%s: asking the time takes no arguments, got %v", path, option.Slots)
		}
		if len(option.Phrases) != len(command.Phrases) {
			t.Errorf("%s: %d phrases reached the model, want %d",
				path, len(option.Phrases), len(command.Phrases))
		}
		for _, phrase := range option.Phrases {
			if strings.TrimSpace(phrase) == "" {
				t.Errorf("%s: an empty phrase would describe nothing", path)
			}
		}
	}
}

func optionFor(t *testing.T, set *intent.Set, id string) struct {
	Phrases  []string
	Slots    []string
	Required []string
} {
	t.Helper()
	for _, option := range commandOptions(set) {
		if option.ID == id {
			return struct {
				Phrases  []string
				Slots    []string
				Required []string
			}{option.Phrases, option.Slots, option.Required}
		}
	}
	t.Fatalf("%s never reached the model", id)
	return struct {
		Phrases  []string
		Slots    []string
		Required []string
	}{}
}
