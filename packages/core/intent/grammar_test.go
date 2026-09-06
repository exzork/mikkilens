package intent_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/exzork/mikkilens/packages/core/intent"
	"github.com/exzork/mikkilens/packages/core/paths"
)

// The shipped Indonesian phrases are exercised directly: if someone edits
// commands.id.toml into an ambiguous state, this is what should catch it.

func shipped(t *testing.T, language string) *intent.Set {
	t.Helper()
	set, err := intent.SetFromFile(paths.CommandsFile(language))
	if err != nil {
		t.Fatalf("could not load commands.%s.toml: %v", language, err)
	}
	return set
}

func build(t *testing.T, commands map[string]any) *intent.Set {
	t.Helper()
	set, err := intent.SetFromMap(map[string]any{"commands": commands}, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return set
}

func phrases(values ...string) []any {
	entries := make([]any, 0, len(values))
	for _, value := range values {
		entries = append(entries, value)
	}
	return entries
}

// -- normalization ------------------------------------------------------------

func TestNormalizeStripsCasePunctuationAndSpacing(t *testing.T) {
	cases := map[string]string{
		"Matikan mikrofon.":               "matikan mikrofon",
		"  GANTI   ke   Just Chatting!  ": "ganti ke just chatting",
		"Berapa penontonnya?":             "berapa penontonnya",
	}
	for raw, want := range cases {
		if got := intent.Normalize(raw); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", raw, got, want)
		}
	}
}

// -- the shipped command set --------------------------------------------------

func TestShippedCommandsLoadWithoutWarnings(t *testing.T) {
	set := shipped(t, "id")
	if set.Len() < 20 {
		t.Errorf("expected at least 20 commands, got %d", set.Len())
	}
	if len(set.Warnings) > 0 {
		t.Errorf("commands.id.toml has problems: %v", set.Warnings)
	}
}

func sampleFor(slot string) string {
	switch slot {
	case "scene":
		return "just chatting"
	case "source":
		return "kamera depan"
	case "text":
		return "main game malam ini"
	case "question":
		return "ada tulisan merah"
	case "amount":
		return "lima puluh persen"
	default:
		return "sesuatu"
	}
}

func sampleEnglishFor(slot string) string {
	switch slot {
	case "scene":
		return "just chatting"
	case "source":
		return "front camera"
	case "text":
		return "playing games tonight"
	case "question":
		return "is there an error"
	case "amount":
		return "fifty percent"
	default:
		return "something"
	}
}

// TestEveryShippedPhraseMatchesItsOwnCommand is the strongest check there is:
// no phrase may be stolen by another command.
func TestEveryShippedPhraseMatchesItsOwnCommand(t *testing.T) {
	for _, language := range []string{"id", "en"} {
		set := shipped(t, language)
		sample := sampleFor
		if language == "en" {
			sample = sampleEnglishFor
		}
		for _, id := range set.Order {
			for _, phrase := range set.Commands[id].Phrases {
				spoken := phrase.Raw
				for _, slot := range phrase.SlotNames {
					spoken = strings.ReplaceAll(spoken, "{"+slot+"}", sample(slot))
				}
				match, rivals := set.Match(spoken)
				switch {
				case len(rivals) > 0:
					names := []string{}
					for _, rival := range rivals {
						names = append(names, rival.Command)
					}
					t.Errorf("[%s] %q expected %s, got ambiguous:%s",
						language, spoken, id, strings.Join(names, ","))
				case match == nil:
					t.Errorf("[%s] %q expected %s, got no-match", language, spoken, id)
				case match.Command != id:
					t.Errorf("[%s] %q expected %s, got %s", language, spoken, id, match.Command)
				}
			}
		}
	}
}

func TestRealAndMisheardPhrasesRouteCorrectly(t *testing.T) {
	set := shipped(t, "id")
	cases := []struct{ spoken, want string }{
		{"matikan mikrofon", "mute_mic"},
		{"mute mic", "mute_mic"},
		{"ganti channel ke musik", "switch_channel"},
		{"berapa penontonnya", "viewer_count"},
		{"hentikan siaran", "stop_stream"},
		{"status", "status"},
		// Real mishearings observed from Whisper on Indonesian speech.
		{"matiin mikrofon", "mute_mic"},
		{"Hantikan siaran", "stop_stream"},
		{"rankum chat", "chat_summarize"},
		{"Berhenti baca kancek", "chat_pause"},
	}
	for _, testCase := range cases {
		match, rivals := set.Match(testCase.spoken)
		if match == nil {
			t.Errorf("%q did not match (rivals: %d)", testCase.spoken, len(rivals))
			continue
		}
		if match.Command != testCase.want {
			t.Errorf("%q matched %s, want %s", testCase.spoken, match.Command, testCase.want)
		}
	}
}

func TestUnrelatedSpeechDoesNotMatch(t *testing.T) {
	// She talks constantly while streaming; ordinary chatter must not fire commands.
	set := shipped(t, "id")
	for _, spoken := range []string{
		"cuaca hari ini bagus sekali ya teman teman",
		"makasih banyak buat semuanya",
		"aku lagi mikirin mau makan apa nanti",
	} {
		if match, rivals := set.Match(spoken); match != nil || len(rivals) > 0 {
			t.Errorf("%q wrongly matched", spoken)
		}
	}
}

func TestALiteralPhraseBeatsAGreedySlot(t *testing.T) {
	// "apa yang ada di layar" must not be read as "{question} di layar".
	set := shipped(t, "id")
	match, _ := set.Match("apa yang ada di layar")
	if match == nil || match.Command != "ask_screen" {
		t.Fatalf("expected ask_screen, got %+v", match)
	}
	if len(match.Slots) != 0 {
		t.Errorf("the literal phrase should not capture a slot, got %v", match.Slots)
	}
}

func TestSlotStillCapturesARealQuestion(t *testing.T) {
	set := shipped(t, "id")
	match, _ := set.Match("ada tulisan merah di layar")
	if match == nil || match.Command != "ask_screen" {
		t.Fatalf("expected ask_screen, got %+v", match)
	}
	if match.Slots["question"] != "ada tulisan merah" {
		t.Errorf("question slot = %q", match.Slots["question"])
	}
}

func TestConfirmFlagIsReadFromTheFile(t *testing.T) {
	set := shipped(t, "id")
	if !set.Commands["stop_stream"].Confirm {
		t.Error("stop_stream must ask first")
	}
	if !set.Commands["set_title"].Confirm {
		t.Error("set_title must ask first")
	}
	if set.Commands["mute_mic"].Confirm {
		t.Error("mute_mic must not ask")
	}
}

// -- slots --------------------------------------------------------------------

func TestTrailingSlotCapturesTheRemainder(t *testing.T) {
	set := build(t, map[string]any{
		"switch_channel": map[string]any{"phrases": phrases("ganti channel ke {channel}")},
	})
	match, _ := set.Match("ganti channel ke musik dan podcast")
	if match == nil || match.Slots["channel"] != "musik dan podcast" {
		t.Fatalf("got %+v", match)
	}
}

func TestLeadingSlotCapturesTheBeginning(t *testing.T) {
	set := build(t, map[string]any{
		"ask": map[string]any{"phrases": phrases("{question} di layar")},
	})
	match, _ := set.Match("apakah ada error di layar")
	if match == nil || match.Slots["question"] != "apakah ada error" {
		t.Fatalf("got %+v", match)
	}
}

func TestSlotInTheMiddle(t *testing.T) {
	set := build(t, map[string]any{
		"setter": map[string]any{"phrases": phrases("ganti judul jadi {text} sekarang")},
	})
	match, _ := set.Match("ganti judul jadi main valorant sekarang")
	if match == nil || match.Slots["text"] != "main valorant" {
		t.Fatalf("got %+v", match)
	}
}

func TestAnEmptySlotDoesNotMatch(t *testing.T) {
	set := build(t, map[string]any{
		"search_music": map[string]any{"phrases": phrases("putar lagu {query}")},
	})
	if match, rivals := set.Match("putar lagu"); match != nil || len(rivals) > 0 {
		t.Errorf("an empty slot must not match, got %+v", match)
	}
}

func TestScoresStayWithinRange(t *testing.T) {
	set := shipped(t, "id")
	for _, spoken := range []string{"matikan mikrofon", "ganti channel ke musik", "apa yang ada di layar"} {
		match, _ := set.Match(spoken)
		if match == nil {
			t.Fatalf("%q did not match", spoken)
		}
		if match.Score < 0 || match.Score > 100 {
			t.Errorf("%q scored %f, outside 0-100", spoken, match.Score)
		}
		if match.Score < intent.DefaultThreshold {
			t.Errorf("%q scored %f, below the threshold", spoken, match.Score)
		}
	}
}

// -- validation ---------------------------------------------------------------

func TestAmbiguousPhrasesAcrossCommandsAreReported(t *testing.T) {
	set := build(t, map[string]any{
		"one": map[string]any{"phrases": phrases("matikan mikrofon")},
		"two": map[string]any{"phrases": phrases("matikan mikropon")},
	})
	if len(set.Warnings) == 0 {
		t.Error("near-identical phrases in two commands must warn")
	}
}

func TestASlotOnlyPhraseIsRejectedAsMatchingEverything(t *testing.T) {
	set := build(t, map[string]any{
		"greedy": map[string]any{"phrases": phrases("{question}")},
		"real":   map[string]any{"phrases": phrases("status")},
	})
	if !anyContains(set.Warnings, "matches everything") && !anyContains(set.Warnings, "only a slot") {
		t.Errorf("expected a warning about matching everything, got %v", set.Warnings)
	}
	match, _ := set.Match("status")
	if match == nil || match.Command != "real" {
		t.Errorf("got %+v", match)
	}
}

func TestACommandWithNoPhrasesIsSkippedWithAWarning(t *testing.T) {
	set := build(t, map[string]any{
		"empty": map[string]any{"phrases": phrases()},
		"real":  map[string]any{"phrases": phrases("status")},
	})
	if set.Has("empty") {
		t.Error("a command with no phrases must be skipped")
	}
	if !anyContains(set.Warnings, "no phrases") {
		t.Errorf("expected a 'no phrases' warning, got %v", set.Warnings)
	}
}

func TestUnknownSlotNamesAreReportedButStillUsable(t *testing.T) {
	set := build(t, map[string]any{
		"odd": map[string]any{"phrases": phrases("atur {wibble}")},
	})
	if !anyContains(set.Warnings, "unknown slot") {
		t.Errorf("expected an 'unknown slot' warning, got %v", set.Warnings)
	}
}

func TestAFileWithNoCommandsSectionFailsLoudly(t *testing.T) {
	if _, err := intent.SetFromMap(map[string]any{"nope": map[string]any{}}, ""); err == nil {
		t.Error("expected an error")
	}
}

func TestAFileWithOnlyBrokenCommandsFailsLoudly(t *testing.T) {
	_, err := intent.SetFromMap(map[string]any{
		"commands": map[string]any{"broken": map[string]any{"phrases": phrases()}},
	}, "")
	if err == nil {
		t.Error("expected an error")
	}
}

func TestAMissingFileFailsLoudly(t *testing.T) {
	if _, err := intent.SetFromFile(filepath.Join(t.TempDir(), "absent.toml")); err == nil {
		t.Error("expected an error")
	}
}

func TestInvalidTomlFailsLoudly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "commands.toml")
	if err := os.WriteFile(path, []byte("[commands.x\nphrases = "), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := intent.SetFromFile(path); err == nil {
		t.Error("expected an error")
	}
}

// The four volumes are told apart by one word in the middle of an otherwise
// identical sentence, which is exactly the shape recognition is worst at. If
// "atur volume musik" ever starts turning her voice down, this is what says so.
func TestTheVolumeCommandsAreNotConfusedWithEachOther(t *testing.T) {
	set := shipped(t, "id")
	for spoken, want := range map[string]string{
		"atur volume bicara lima puluh persen": "set_speech_volume",
		"atur volume chat lima puluh persen":   "set_chat_volume",
		"atur volume nada lima puluh persen":   "set_earcon_volume",
		"atur volume musik lima puluh persen":  "set_music_volume",
	} {
		match, rivals := set.Match(spoken)
		if len(rivals) > 0 || match == nil {
			t.Errorf("%q did not resolve to one command", spoken)
			continue
		}
		if match.Command != want {
			t.Errorf("%q matched %s, want %s", spoken, match.Command, want)
		}
		if got := match.Slots["amount"]; got != "lima puluh persen" {
			t.Errorf("%q captured amount %q", spoken, got)
		}
	}
}

// -- both languages -----------------------------------------------------------

func TestEnglishCommandsLoadWithoutWarnings(t *testing.T) {
	if warnings := shipped(t, "en").Warnings; len(warnings) > 0 {
		t.Errorf("commands.en.toml has problems: %v", warnings)
	}
}

// TestBothLanguagesDefineTheSameCommands: switching language must not silently
// lose a command.
func TestBothLanguagesDefineTheSameCommands(t *testing.T) {
	indonesian, english := shipped(t, "id"), shipped(t, "en")
	for id := range indonesian.Commands {
		if !english.Has(id) {
			t.Errorf("commands.en.toml is missing %q", id)
		}
	}
	for id := range english.Commands {
		if !indonesian.Has(id) {
			t.Errorf("commands.id.toml is missing %q", id)
		}
	}
}

func anyContains(values []string, needle string) bool {
	for _, value := range values {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}
