package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/exzork/mikkilens/packages/core/config"
	"github.com/exzork/mikkilens/packages/core/i18n"
)

// Small models are not reliably tidy. They wrap JSON in code fences, put a
// sentence in front of it, or answer with prose alone. None of that should
// turn into a wrong command, and none of it should turn a usable answer into
// a refusal either.

func TestPlainJSONIsRead(t *testing.T) {
	guess := parseGuess(`{"command": "mute_mic", "slots": {}}`)

	if guess.Command != "mute_mic" {
		t.Errorf("command is %q", guess.Command)
	}
}

func TestACodeFenceIsToleratedRatherThanRefused(t *testing.T) {
	guess := parseGuess("```json\n{\"command\": \"chat_pause\", \"slots\": {}}\n```")

	if guess.Command != "chat_pause" {
		t.Errorf("command is %q; a fence is the commonest wrapper there is", guess.Command)
	}
}

func TestASentenceBeforeTheJSONIsIgnored(t *testing.T) {
	guess := parseGuess(`Sure! Here is the result:
{"command": "go_live", "slots": {}}`)

	if guess.Command != "go_live" {
		t.Errorf("command is %q", guess.Command)
	}
}

func TestSlotsAreRead(t *testing.T) {
	guess := parseGuess(`{"command":"set_title","slots":{"text":"main minecraft"}}`)

	if guess.Command != "set_title" {
		t.Fatalf("command is %q", guess.Command)
	}
	if guess.Slots["text"] != "main minecraft" {
		t.Errorf("text slot is %q", guess.Slots["text"])
	}
}

func TestBlankSlotsAreDropped(t *testing.T) {
	guess := parseGuess(`{"command":"set_title","slots":{"text":"  ","scene":""}}`)

	if len(guess.Slots) != 0 {
		t.Errorf("kept %v; a blank slot is not a value", guess.Slots)
	}
}

// Refusing is a designed answer, not a malfunction.
func TestAnEmptyCommandMeansItRecognisedNothing(t *testing.T) {
	guess := parseGuess(`{"command": "", "slots": {}}`)

	if guess.Command != "" {
		t.Errorf("command is %q, want empty", guess.Command)
	}
}

// Prose with no JSON at all must come to nothing. Reading a command out of a
// sentence would be guessing on top of a guess.
func TestProseWithNoJSONIsNotAMatch(t *testing.T) {
	for _, answer := range []string{
		"I think she wants to mute the microphone.",
		"mute_mic",
		"",
		"   ",
	} {
		if guess := parseGuess(answer); guess.Command != "" {
			t.Errorf("parseGuess(%q) found %q, want nothing", answer, guess.Command)
		}
	}
}

func TestBrokenJSONIsNotAMatch(t *testing.T) {
	for _, answer := range []string{
		`{"command": "mute_mic"`,
		`{"command": ["mute_mic"]}`,
		`}{`,
	} {
		if guess := parseGuess(answer); guess.Command != "" {
			t.Errorf("parseGuess(%q) found %q, want nothing", answer, guess.Command)
		}
	}
}

// The prompt is what the model actually sees, so the things it must contain
// are worth pinning: the ids it may choose from, the phrases that describe
// them, and permission to refuse.
func TestThePromptOffersTheCommandsAndAllowsRefusing(t *testing.T) {
	prompt := matchSystemPrompt([]CommandOption{
		{ID: "mute_mic", Phrases: []string{"matikan mikrofon", "mute mic"}},
		{ID: "set_title", Phrases: []string{"ganti judul jadi {text}"}, Slots: []string{"text"}},
	})

	for _, needed := range []string{
		"mute_mic", "matikan mikrofon", "set_title", "text",
	} {
		if !contains(prompt, needed) {
			t.Errorf("the prompt never mentions %q", needed)
		}
	}
	if !contains(prompt, `{"command": ""}`) {
		t.Error("the model must be told it may recognise nothing, " +
			"or it will always pick something")
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for index := 0; index+len(needle) <= len(haystack); index++ {
		if haystack[index:index+len(needle)] == needle {
			return index
		}
	}
	return -1
}

// -- the whole path -----------------------------------------------------------

// Proves the wiring end to end without a model: config resolves the matcher
// endpoint, the request is shaped the way an OpenAI-compatible server expects,
// and the reply comes back as a command.
func TestMatchCommandTalksToAnOpenAICompatibleServer(t *testing.T) {
	var seen struct {
		path   string
		model  string
		system string
		user   string
	}

	server := httptest.NewServer(http.HandlerFunc(
		func(writer http.ResponseWriter, request *http.Request) {
			seen.path = request.URL.Path

			var body struct {
				Model    string `json:"model"`
				Messages []struct {
					Role    string `json:"role"`
					Content string `json:"content"`
				} `json:"messages"`
			}
			_ = json.NewDecoder(request.Body).Decode(&body)
			seen.model = body.Model
			for _, message := range body.Messages {
				switch message.Role {
				case "system":
					seen.system = message.Content
				case "user":
					seen.user = message.Content
				}
			}

			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(
				`{"choices":[{"message":{"content":"{\"command\":\"chat_pause\",\"slots\":{}}"}}]}`))
		}))
	defer server.Close()

	settings := config.Default()
	settings.Matcher = config.Matcher{
		Enabled: true, Base: server.URL + "/v1", Model: "gemma3n:e2b",
	}

	guess, err := New(settings, i18n.Load("id")).MatchCommand(
		context.Background(), "tolong jangan bacakan chatnya dulu",
		[]CommandOption{
			{ID: "chat_pause", Phrases: []string{"jeda chat"}},
			{ID: "chat_resume", Phrases: []string{"lanjutkan chat"}},
		})
	if err != nil {
		t.Fatalf("MatchCommand: %v", err)
	}

	if guess.Command != "chat_pause" {
		t.Errorf("command is %q", guess.Command)
	}
	if seen.path != "/v1/chat/completions" {
		t.Errorf("posted to %q", seen.path)
	}
	if seen.model != "gemma3n:e2b" {
		t.Errorf("asked model %q", seen.model)
	}
	if seen.user != "tolong jangan bacakan chatnya dulu" {
		t.Errorf("sent transcript %q", seen.user)
	}
	if !contains(seen.system, "chat_pause") || !contains(seen.system, "jeda chat") {
		t.Error("the commands and their phrases must reach the model")
	}
}

// Without an endpoint it must refuse locally rather than attempting a call, so
// that an unconfigured matcher costs nothing at all.
func TestAnUnconfiguredMatcherRefusesWithoutCallingAnything(t *testing.T) {
	settings := config.Default()
	settings.Matcher = config.Matcher{Enabled: true}

	_, err := New(settings, i18n.Load("id")).MatchCommand(
		context.Background(), "apa saja", []CommandOption{{ID: "mute_mic"}})

	if err == nil {
		t.Fatal("expected a refusal")
	}
}

// Turning it off must actually turn it off, even with a base URL still filled
// in from a previous experiment.
func TestDisablingTheMatcherIgnoresAnEndpointLeftBehind(t *testing.T) {
	settings := config.Default()
	settings.Matcher = config.Matcher{
		Enabled: false, Base: "http://localhost:11434/v1", Model: "gemma3n:e2b",
	}

	if base, model, _ := settings.MatcherEndpoint(); base != "" || model != "" {
		t.Errorf("disabled matcher still resolves to %q/%q", base, model)
	}
}
