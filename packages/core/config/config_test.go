package config_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/exzork/mikkilens/packages/core/config"
	"github.com/exzork/mikkilens/packages/core/paths"
)

// Config loading must be forgiving. A hand-edited config with a typo in it
// should still start and still speak; refusing to boot would leave her with a
// silent machine and no way to see why.

func TestDefaultsAreIndonesian(t *testing.T) {
	settings := config.Default()
	if settings.Language.Output != "id" || settings.Language.STT != "id" {
		t.Errorf("defaults are %+v", settings.Language)
	}
}

func TestEmptyVoiceFallsBackToTheLocaleVoice(t *testing.T) {
	settings := config.Default()
	if got := settings.Voice("id-ID-GadisNeural"); got != "id-ID-GadisNeural" {
		t.Errorf("Voice() = %q", got)
	}
	settings.Speech.Voice = "id-ID-ArdiNeural"
	if got := settings.Voice("id-ID-GadisNeural"); got != "id-ID-ArdiNeural" {
		t.Errorf("Voice() = %q", got)
	}
}

func TestChatVoiceDefaultsToTheMainVoice(t *testing.T) {
	settings := config.Default()
	settings.Speech.Voice = "id-ID-ArdiNeural"
	if got := settings.VoiceForChat("x"); got != "id-ID-ArdiNeural" {
		t.Errorf("VoiceForChat() = %q", got)
	}
	settings.Speech.ChatVoice = "id-ID-GadisNeural"
	if got := settings.VoiceForChat("x"); got != "id-ID-GadisNeural" {
		t.Errorf("VoiceForChat() = %q", got)
	}
}

// TestDonationVoiceFallsBackToTheMainVoiceNotTheChatVoice: an unconfigured
// donation must not arrive sounding like one more chat message.
func TestDonationVoiceFallsBackToTheMainVoiceNotTheChatVoice(t *testing.T) {
	settings := config.Default()
	settings.Speech.Voice = "id-ID-ArdiNeural"
	settings.Speech.ChatVoice = "id-ID-GadisNeural"
	if got := settings.VoiceForDonation("x"); got != "id-ID-ArdiNeural" {
		t.Errorf("VoiceForDonation() = %q, want the main voice", got)
	}
	settings.Speech.DonationVoice = "en-US-AriaNeural"
	if got := settings.VoiceForDonation("x"); got != "en-US-AriaNeural" {
		t.Errorf("VoiceForDonation() = %q", got)
	}
}

func TestUnknownKeysAreIgnoredRatherThanFatal(t *testing.T) {
	settings := config.FromMap(map[string]any{
		"speech": map[string]any{"rate": "+20%", "typo_here": int64(1)},
	})
	if settings.Speech.Rate != "+20%" {
		t.Errorf("rate = %q", settings.Speech.Rate)
	}
}

func TestUnknownSectionsAreIgnored(t *testing.T) {
	settings := config.FromMap(map[string]any{
		"not_a_section": map[string]any{"a": int64(1)},
		"language":      map[string]any{"output": "en"},
	})
	if settings.Language.Output != "en" {
		t.Errorf("output = %q", settings.Language.Output)
	}
}

func TestPartialSectionKeepsOtherDefaults(t *testing.T) {
	settings := config.FromMap(map[string]any{"language": map[string]any{"output": "en"}})
	if settings.Language.STT != "id" {
		t.Errorf("unspecified keys must keep their defaults, stt = %q", settings.Language.STT)
	}
}

func TestRoundTripThroughTomlIsStable(t *testing.T) {
	original := config.FromMap(map[string]any{
		"speech": map[string]any{"rate": "+20%"},
		"chat":   map[string]any{"muted_users": []any{"bot"}},
	})
	path := filepath.Join(t.TempDir(), "config.toml")
	if _, err := original.Save(path); err != nil {
		t.Fatal(err)
	}
	reloaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(original, reloaded) {
		t.Errorf("round trip changed the config:\n%+v\n%+v", original, reloaded)
	}
}

func TestSaveIsAtomicAndLeavesNoTempFile(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "config.toml")
	if _, err := config.Default().Save(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	leftovers, _ := filepath.Glob(filepath.Join(directory, "*.tmp"))
	if len(leftovers) > 0 {
		t.Errorf("temp files left behind: %v", leftovers)
	}
}

func TestInvalidTomlRaisesAClearError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("this is not = = valid toml"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(path); err == nil {
		t.Error("expected an error")
	}
}

func TestMissingFileYieldsDefaults(t *testing.T) {
	settings, err := config.Load(filepath.Join(t.TempDir(), "absent.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(settings, config.Default()) {
		t.Error("a missing file must yield the defaults")
	}
}

func TestOneEndpointServesEverything(t *testing.T) {
	t.Setenv("MIKKILENS_MODEL_KEY", "secret-value")
	settings := config.FromMap(map[string]any{
		"model": map[string]any{"base_url": "https://example/v1", "model": "gpt-4o-mini"},
	})
	base, model, key := settings.ModelEndpoint()
	if base != "https://example/v1" || model != "gpt-4o-mini" || key != "secret-value" {
		t.Errorf("got %q %q %q", base, model, key)
	}
}

func TestTheModelIsNotConsideredConfiguredWithoutBothHalves(t *testing.T) {
	without := config.FromMap(map[string]any{"model": map[string]any{"base_url": "https://x/v1"}})
	if without.Model.Configured() {
		t.Error("a base_url alone is not a configured provider")
	}
	with := config.FromMap(map[string]any{
		"model": map[string]any{"base_url": "https://x/v1", "model": "m"},
	})
	if !with.Model.Configured() {
		t.Error("base_url plus model is configured")
	}
}

// The sections that used to hold endpoints of their own are gone. A config
// still carrying them must start rather than fail, because the file on her
// machine was written by the previous version and the warning goes unheard.
func TestAConfigFromTheOlderLayoutStillStarts(t *testing.T) {
	settings := config.FromMap(map[string]any{
		"vision": map[string]any{"base_url": "https://old/v1", "model": "old", "max_edge": 800},
		"llm":    map[string]any{"base_url": "https://old/v1"},
		"youtube": map[string]any{
			"enabled": true, "api_key_env": "MIKKILENS_YOUTUBE_KEY", "video_id": "abc",
		},
	})
	if settings.Vision.MaxEdge != 800 {
		t.Errorf("a key that still exists was lost: max_edge = %d", settings.Vision.MaxEdge)
	}
	if settings.Model.Configured() {
		t.Error("an endpoint from the old layout must not be adopted silently")
	}
}

// -- secrets ------------------------------------------------------------------

func TestSecretsComeFromTheEnvironmentFirst(t *testing.T) {
	paths.SetRoot(t.TempDir())
	t.Setenv("MY_KEY", "from-env")
	if err := config.StoreSecret("MY_KEY", "from-file"); err != nil {
		t.Fatal(err)
	}
	if got := config.ResolveSecret("MY_KEY"); got != "from-env" {
		t.Errorf("ResolveSecret() = %q, want the environment value", got)
	}
	t.Setenv("MY_KEY", "")
	if got := config.ResolveSecret("MY_KEY"); got != "from-file" {
		t.Errorf("ResolveSecret() = %q, want the stored value", got)
	}
}

func TestSecretsNeverLandInTheConfigFile(t *testing.T) {
	directory := t.TempDir()
	paths.SetRoot(directory)
	if err := config.StoreSecret("MY_KEY", "super-secret"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "config.toml")
	if _, err := config.Default().Save(path); err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(written), "super-secret") {
		t.Error("a secret reached config.toml")
	}
}

func TestStoringAnEmptySecretRemovesIt(t *testing.T) {
	paths.SetRoot(t.TempDir())
	if err := config.StoreSecret("MY_KEY", "value"); err != nil {
		t.Fatal(err)
	}
	if err := config.StoreSecret("MY_KEY", ""); err != nil {
		t.Fatal(err)
	}
	if got := config.ResolveSecret("MY_KEY"); got != "" {
		t.Errorf("ResolveSecret() = %q, want empty", got)
	}
}

// -- bindings -----------------------------------------------------------------

// A bound key is what a Stream Deck, a foot pedal or a mouse macro presses.
// The bindings live in the same file she edits by hand, so they have to
// survive being written back by the settings page unchanged -- a key that
// stops working after someone saves an unrelated setting is a key she cannot
// rely on mid-stream.

func TestBindingsSurviveASaveAndReload(t *testing.T) {
	never := false
	original := config.Default()
	original.Bindings = []config.Binding{
		{Combination: "<ctrl>+<alt>+<f13>", Command: "go_live"},
		{Combination: "<ctrl>+<alt>+<f14>", Command: "stop_stream", Confirm: &never},
	}

	path := filepath.Join(t.TempDir(), "config.toml")
	if _, err := original.Save(path); err != nil {
		t.Fatal(err)
	}
	reloaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}

	if len(reloaded.Bindings) != 2 {
		t.Fatalf("bindings = %+v, want two", reloaded.Bindings)
	}
	if reloaded.Bindings[0].Command != "go_live" {
		t.Errorf("first binding = %+v", reloaded.Bindings[0])
	}
	if reloaded.Bindings[1].Confirm == nil || *reloaded.Bindings[1].Confirm {
		t.Errorf("a binding that waives the question must keep waiving it: %+v",
			reloaded.Bindings[1])
	}
}

func TestABindingWithoutConfirmLeavesTheCommandsOwnGateAlone(t *testing.T) {
	settings := config.FromMap(map[string]any{
		"bindings": []any{
			map[string]any{"combination": "<ctrl>+<alt>+<f13>", "command": "stop_stream"},
		},
	})
	if len(settings.Bindings) != 1 {
		t.Fatalf("bindings = %+v, want one", settings.Bindings)
	}
	// nil, not false: unset means "whatever the command itself says", which
	// for stopping a stream means it still asks.
	if settings.Bindings[0].Confirm != nil {
		t.Errorf("Confirm = %v, want unset", *settings.Bindings[0].Confirm)
	}
}

func TestBindingsAreNotWarnedAboutAsAnUnknownSection(t *testing.T) {
	// [[bindings]] is a list of tables rather than a section, and the check
	// that walks the config's sections used to assume every one was a table.
	settings := config.FromMap(map[string]any{
		"bindings": []any{map[string]any{"combination": "<ctrl>+x", "command": "status"}},
		"speech":   map[string]any{"rate": "+20%"},
	})
	if settings.Speech.Rate != "+20%" {
		t.Errorf("rate = %q; the rest of the config must still load", settings.Speech.Rate)
	}
}
