package i18n_test

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/exzork/mikkilens/packages/core/i18n"
)

// A missing key would surface as either silence or a spoken
// placeholder in the middle of a stream, so the key set is checked
// mechanically rather than by eye: every locale must carry the same keys, and
// every key the code actually asks for must exist.

func TestAtLeastIndonesianAndEnglishShip(t *testing.T) {
	available := map[string]bool{}
	for _, language := range i18n.Available() {
		available[language] = true
	}
	for _, wanted := range []string{"id", "en"} {
		if !available[wanted] {
			t.Errorf("%s.toml does not ship", wanted)
		}
	}
}

func TestEveryLocaleHasTheSameKeys(t *testing.T) {
	reference := keysOf(i18n.Load(i18n.FallbackLanguage))
	for _, language := range i18n.Available() {
		keys := keysOf(i18n.Load(language))
		missing, extra := difference(reference, keys), difference(keys, reference)
		if len(missing) > 0 || len(extra) > 0 {
			t.Errorf("%s.toml differs from %s.toml. Missing: %v. Extra: %v",
				language, i18n.FallbackLanguage, missing, extra)
		}
	}
}

func TestLocaleDeclaresRequiredMetadata(t *testing.T) {
	for _, language := range i18n.Available() {
		locale := i18n.Load(language)
		if locale.DefaultVoice() == "" {
			t.Errorf("%s.toml has no [meta] voice", language)
		}
		if len(locale.YesWords()) == 0 || len(locale.NoWords()) == 0 {
			t.Errorf("%s.toml is missing yes or no words", language)
		}
		yes := map[string]bool{}
		for _, word := range locale.YesWords() {
			yes[word] = true
		}
		for _, word := range locale.NoWords() {
			if yes[word] {
				t.Errorf("%s.toml: %q cannot mean both yes and no", language, word)
			}
		}
	}
}

// keyCall finds l.T("obs.connected"), SayKey("chat.paused"), Resolve(...) and
// the like, so a key referenced in code but never defined is caught here
// rather than mid-stream.
var keyCall = regexp.MustCompile(`(?:\.T|SayKey|SayKeyAt|\.Resolve|Error)\(\s*"([a-z_]+\.[a-z_]+)"`)

func TestEveryKeyUsedInCodeExists(t *testing.T) {
	known := keysOf(i18n.Load(i18n.FallbackLanguage))
	root := repoRoot(t)

	missing := map[string][]string{}
	err := filepath.Walk(filepath.Join(root, "packages"), func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		source, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, found := range keyCall.FindAllStringSubmatch(string(source), -1) {
			if !known[found[1]] {
				relative, _ := filepath.Rel(root, path)
				missing[relative] = append(missing[relative], found[1])
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) > 0 {
		t.Errorf("locale keys referenced in code but not defined: %v", missing)
	}
}

func TestMissingKeyIsAudibleRatherThanSilent(t *testing.T) {
	locale := i18n.Load("id")
	spoken := locale.T("nope.not_here")
	if strings.TrimSpace(spoken) == "" {
		t.Fatal("a missing key must never resolve to nothing")
	}
	if !strings.Contains(spoken, "not here") {
		t.Errorf("the marker should name the key, got %q", spoken)
	}
	if len(locale.MissingKeys()) == 0 {
		t.Error("the missing key should be recorded for the log page")
	}
}

func TestUnknownLanguageFallsBackInsteadOfCrashing(t *testing.T) {
	if locale := i18n.Load("klingon"); locale.Language != i18n.FallbackLanguage {
		t.Errorf("language = %q", locale.Language)
	}
}

func TestIndonesianFallsBackToEnglishForAGap(t *testing.T) {
	locale := i18n.Load("id")
	delete(locale.Strings, "obs.connected")
	if got, want := locale.T("obs.connected"), i18n.Load("en").T("obs.connected"); got != want {
		t.Errorf("T() = %q, want the English text %q", got, want)
	}
}

func TestResolveAcceptsEitherAKeyOrLiteralText(t *testing.T) {
	locale := i18n.Load("id")
	if got, want := locale.Resolve("confirm.stop_stream"), locale.T("confirm.stop_stream"); got != want {
		t.Errorf("Resolve(key) = %q, want %q", got, want)
	}
	if got := locale.Resolve("Kalimat saya sendiri"); got != "Kalimat saya sendiri" {
		t.Errorf("Resolve(text) = %q", got)
	}
}

func TestFormattingPlaceholdersAreFilled(t *testing.T) {
	locale := i18n.Load("id")
	got := locale.T("channel.switched", i18n.Args{"channel": "musik"})
	if !strings.Contains(got, "musik") {
		t.Errorf("T() = %q", got)
	}
}

func TestABadPlaceholderReturnsTheTemplateNotAnError(t *testing.T) {
	locale := i18n.Load("id")
	if got, want := locale.T("channel.switched"), locale.Strings["channel.switched"]; got != want {
		t.Errorf("T() = %q, want the untouched template %q", got, want)
	}
}

// -- helpers ------------------------------------------------------------------

func keysOf(locale *i18n.Locale) map[string]bool {
	keys := map[string]bool{}
	for key := range locale.Strings {
		keys[key] = true
	}
	return keys
}

func difference(from, without map[string]bool) []string {
	only := []string{}
	for key := range from {
		if !without[key] {
			only = append(only, key)
		}
	}
	sort.Strings(only)
	return only
}

func repoRoot(t *testing.T) string {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(directory, "go.mod")); err == nil {
			return directory
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			t.Fatal("could not find the repository root")
		}
		directory = parent
	}
}
