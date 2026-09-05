package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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
		path             string
		model            string
		system           string
		user             string
		toolChoice       string
		tools            []string
		sceneDescription string
		sceneRequired    []string
		sceneClosed      bool
		sceneHasSlot     bool
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
				ToolChoice string `json:"tool_choice"`
				Tools      []struct {
					Type     string `json:"type"`
					Function struct {
						Name        string `json:"name"`
						Description string `json:"description"`
						Parameters  struct {
							Type       string `json:"type"`
							Properties map[string]struct {
								Type        string `json:"type"`
								Description string `json:"description"`
							} `json:"properties"`
							Required             []string `json:"required"`
							AdditionalProperties bool     `json:"additionalProperties"`
						} `json:"parameters"`
					} `json:"function"`
				} `json:"tools"`
			}
			_ = json.NewDecoder(request.Body).Decode(&body)
			seen.model = body.Model
			seen.toolChoice = body.ToolChoice
			for _, message := range body.Messages {
				switch message.Role {
				case "system":
					seen.system = message.Content
				case "user":
					seen.user = message.Content
				}
			}
			for _, tool := range body.Tools {
				seen.tools = append(seen.tools, tool.Function.Name)
				if tool.Function.Name == "switch_scene" {
					seen.sceneDescription = tool.Function.Description
					seen.sceneRequired = tool.Function.Parameters.Required
					seen.sceneClosed = !tool.Function.Parameters.AdditionalProperties
					_, seen.sceneHasSlot = tool.Function.Parameters.Properties["scene"]
				}
			}

			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"choices":[{"message":{"content":"",` +
				`"tool_calls":[{"type":"function","function":` +
				`{"name":"chat_pause","arguments":"{}"}}]}}]}`))
		}))
	defer server.Close()

	settings := config.Default()
	settings.Model = config.Model{Base: server.URL + "/v1", Model: "gemma3n:e2b"}
	settings.Matcher = config.Matcher{Enabled: true}

	guess, err := New(settings, i18n.Load("id")).MatchCommand(
		context.Background(), "tolong jangan bacakan chatnya dulu",
		[]CommandOption{
			{ID: "chat_pause", Phrases: []string{"jeda chat"}},
			{ID: "chat_resume", Phrases: []string{"lanjutkan chat"}},
			{ID: "switch_scene", Phrases: []string{"ganti ke {scene}"},
				Slots: []string{"scene"}, Required: []string{"scene"}},
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

	// Every command reaches the model as a tool, not as a list in the prompt.
	if len(seen.tools) != 3 {
		t.Errorf("offered %d tools, want 3: %v", len(seen.tools), seen.tools)
	}
	if seen.toolChoice != "auto" {
		t.Errorf("tool_choice is %q, want auto so it can call nothing", seen.toolChoice)
	}

	// The phrases are the description: they are what tells a model when to
	// call this rather than the one next to it.
	if !contains(seen.sceneDescription, "ganti ke {scene}") {
		t.Errorf("the phrases must describe the tool, got %q", seen.sceneDescription)
	}
	if !seen.sceneHasSlot {
		t.Error("a slotted command must declare its slot as an argument")
	}
	if len(seen.sceneRequired) != 1 || seen.sceneRequired[0] != "scene" {
		t.Errorf("required is %v, want [scene]", seen.sceneRequired)
	}
	if !seen.sceneClosed {
		t.Error("additionalProperties must be false so no argument can be invented")
	}
}

// Without an endpoint it must refuse locally rather than attempting a call, so
// that an unconfigured matcher costs nothing at all.
func TestAnUnconfiguredMatcherRefusesWithoutCallingAnything(t *testing.T) {
	settings := config.Default()
	settings.Matcher = config.Matcher{Enabled: true} // on, but nothing to ask

	_, err := New(settings, i18n.Load("id")).MatchCommand(
		context.Background(), "apa saja", []CommandOption{{ID: "mute_mic"}})

	if err == nil {
		t.Fatal("expected a refusal")
	}
}

// Turning it off must actually turn it off. It shares the endpoint with
// everything else now, so "off" cannot mean an empty base URL -- it has to be
// the switch, or unrecognised speech would keep being sent to a provider that
// is configured for summaries and screenshots.
func TestDisablingTheMatcherStopsItUsingTheSharedEndpoint(t *testing.T) {
	settings := config.Default()
	settings.Model = config.Model{Base: "http://localhost:11434/v1", Model: "gemma3n:e2b"}
	settings.Matcher = config.Matcher{Enabled: false}

	if endpoint := New(settings, i18n.Load("id")).MatcherEndpoint(); endpoint.Configured() {
		t.Errorf("a disabled matcher still resolves to %+v", endpoint)
	}
	// The same settings must still serve everything else.
	if !New(settings, i18n.Load("id")).Endpoint().Configured() {
		t.Error("switching the matcher off must not switch the model off")
	}
}

// -- tools ---------------------------------------------------------------

// toolServer answers every call with a fixed body and records the last request.
func toolServer(t *testing.T, reply func(w http.ResponseWriter), seen *[]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(
		func(writer http.ResponseWriter, request *http.Request) {
			var body struct {
				Tools []struct {
					Function struct {
						Name string `json:"name"`
					} `json:"function"`
				} `json:"tools"`
			}
			_ = json.NewDecoder(request.Body).Decode(&body)
			names := []string{}
			for _, tool := range body.Tools {
				names = append(names, tool.Function.Name)
			}
			*seen = append(*seen, strings.Join(names, ","))
			writer.Header().Set("Content-Type", "application/json")
			reply(writer)
		}))
}

func matcherFor(t *testing.T, url string) *Controller {
	t.Helper()
	settings := config.Default()
	settings.Model = config.Model{Base: url + "/v1", Model: "test"}
	settings.Matcher = config.Matcher{Enabled: true}
	return New(settings, i18n.Load("id"))
}

var twoCommands = []CommandOption{
	{ID: "chat_pause", Phrases: []string{"jeda chat"}},
	{ID: "set_title", Phrases: []string{"ganti judul jadi {text}"},
		Slots: []string{"text"}, Required: []string{"text"}},
}

// Calling nothing is how the model refuses, and refusing must survive intact:
// it is the whole reason tool_choice is "auto" rather than "required".
func TestNoToolCallMeansItRecognisedNothing(t *testing.T) {
	var seen []string
	server := toolServer(t, func(w http.ResponseWriter) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"I am not sure."}}]}`))
	}, &seen)
	defer server.Close()

	guess, err := matcherFor(t, server.URL).MatchCommand(
		context.Background(), "cuaca hari ini gimana", twoCommands)
	if err != nil {
		t.Fatalf("MatchCommand: %v", err)
	}
	if guess.Command != "" {
		t.Errorf("command is %q, want a refusal", guess.Command)
	}
}

// The arguments of a call become the slots.
func TestAToolCallsArgumentsBecomeSlots(t *testing.T) {
	var seen []string
	server := toolServer(t, func(w http.ResponseWriter) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"tool_calls":[{"function":` +
			`{"name":"set_title","arguments":"{\"text\":\"main valorant\"}"}}]}}]}`))
	}, &seen)
	defer server.Close()

	guess, err := matcherFor(t, server.URL).MatchCommand(
		context.Background(), "judulnya jadi main valorant", twoCommands)
	if err != nil {
		t.Fatalf("MatchCommand: %v", err)
	}
	if guess.Command != "set_title" {
		t.Fatalf("command is %q", guess.Command)
	}
	if guess.Slots["text"] != "main valorant" {
		t.Errorf("text slot is %q", guess.Slots["text"])
	}
}

// A name that was never offered is the model inventing one, and inventing must
// not reach a handler.
func TestAToolNameThatWasNeverOfferedIsRefused(t *testing.T) {
	var seen []string
	server := toolServer(t, func(w http.ResponseWriter) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"tool_calls":[{"function":` +
			`{"name":"delete_everything","arguments":"{}"}}]}}]}`))
	}, &seen)
	defer server.Close()

	guess, err := matcherFor(t, server.URL).MatchCommand(
		context.Background(), "apa saja", twoCommands)
	if err != nil {
		t.Fatalf("MatchCommand: %v", err)
	}
	if guess.Command != "" {
		t.Errorf("accepted an invented tool %q", guess.Command)
	}
}

// An argument that is not a slot of that command is dropped rather than passed
// on, so a handler never sees something nothing declared.
func TestArgumentsThatAreNotSlotsAreDropped(t *testing.T) {
	var seen []string
	server := toolServer(t, func(w http.ResponseWriter) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"tool_calls":[{"function":` +
			`{"name":"set_title","arguments":"{\"text\":\"halo\",\"scene\":\"nope\"}"}}]}}]}`))
	}, &seen)
	defer server.Close()

	guess, _ := matcherFor(t, server.URL).MatchCommand(
		context.Background(), "judulnya halo", twoCommands)
	if guess.Slots["scene"] != "" {
		t.Errorf("kept an undeclared argument: %v", guess.Slots)
	}
	if guess.Slots["text"] != "halo" {
		t.Errorf("dropped the real slot: %v", guess.Slots)
	}
}

// Two calls means it did not understand the question. Doing the first of
// several commands she never asked for is the exact failure to avoid.
func TestSeveralToolCallsAreRefusedRatherThanPicked(t *testing.T) {
	var seen []string
	server := toolServer(t, func(w http.ResponseWriter) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"tool_calls":[` +
			`{"function":{"name":"chat_pause","arguments":"{}"}},` +
			`{"function":{"name":"set_title","arguments":"{\"text\":\"x\"}"}}]}}]}`))
	}, &seen)
	defer server.Close()

	guess, _ := matcherFor(t, server.URL).MatchCommand(
		context.Background(), "jeda chat dan ganti judul", twoCommands)
	if guess.Command != "" {
		t.Errorf("picked %q out of two calls", guess.Command)
	}
}

// A server with no tool support must not cost her the command. It should fall
// back to the prompt, and not pay the failed round trip again next time.
func TestAServerWithoutToolSupportFallsBackToThePrompt(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(
		func(writer http.ResponseWriter, request *http.Request) {
			var body struct {
				Tools []any `json:"tools"`
			}
			_ = json.NewDecoder(request.Body).Decode(&body)
			calls++

			writer.Header().Set("Content-Type", "application/json")
			if len(body.Tools) > 0 {
				writer.WriteHeader(http.StatusBadRequest)
				_, _ = writer.Write([]byte(
					`{"error":{"message":"Unrecognized request argument supplied: tools"}}`))
				return
			}
			_, _ = writer.Write([]byte(
				`{"choices":[{"message":{"content":"{\"command\":\"chat_pause\",\"slots\":{}}"}}]}`))
		}))
	defer server.Close()

	client := matcherFor(t, server.URL)
	guess, err := client.MatchCommand(context.Background(), "jangan bacakan chat", twoCommands)
	if err != nil {
		t.Fatalf("MatchCommand: %v", err)
	}
	if guess.Command != "chat_pause" {
		t.Fatalf("command is %q, want the prompt fallback to have answered", guess.Command)
	}
	if calls != 2 {
		t.Fatalf("made %d calls, want 2: one refused, one fallback", calls)
	}

	// Second time it must go straight to the prompt: this sits in the way of a
	// command she has already spoken, and a wasted round trip is a wasted second.
	if _, err := client.MatchCommand(
		context.Background(), "jangan bacakan chat", twoCommands); err != nil {
		t.Fatalf("second MatchCommand: %v", err)
	}
	if calls != 3 {
		t.Errorf("made %d calls in total, want 3: the refusal must be remembered", calls)
	}
}
