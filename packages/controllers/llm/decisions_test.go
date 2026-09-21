package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/exzork/mikkilens/packages/core/config"
	"github.com/exzork/mikkilens/packages/core/i18n"
)

// The decision model is allowed to be wrong, unreachable, or unsure. None of
// those may cost her a command that would otherwise have run, so every one of
// them has to come out as the ordinary text matcher rather than as a failure.
//
// The one thing it must never do is run a command she did not ask for, which
// is what the confidence threshold is for and what most of this checks.

// seenDecision records the request the decision provider was sent.
type seenDecision struct {
	mu        sync.Mutex
	called    int
	path      string
	model     string
	state     string
	kind      string
	criteria  map[string]string
	questions []string
}

func (s *seenDecision) snapshot() seenDecision {
	s.mu.Lock()
	defer s.mu.Unlock()
	return seenDecision{
		called: s.called, path: s.path, model: s.model, state: s.state,
		kind: s.kind, criteria: s.criteria, questions: s.questions,
	}
}

// decisionServer answers with a fixed body and records what it was asked.
func decisionServer(t *testing.T, body string, seen *seenDecision) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(
		func(writer http.ResponseWriter, request *http.Request) {
			var parsed decisionRequest
			_ = json.NewDecoder(request.Body).Decode(&parsed)

			seen.mu.Lock()
			seen.called++
			seen.path = request.URL.Path
			seen.model = parsed.Model
			seen.state = parsed.State
			for name, question := range parsed.Questions {
				seen.questions = append(seen.questions, name)
				seen.kind = question.Type
				seen.criteria = question.Criteria
			}
			seen.mu.Unlock()

			writer.Header().Set("Content-Type", "application/json")
			fmt.Fprint(writer, body)
		}))
}

// chosen builds the provider's answer for a choice question.
func chosen(key string, confidence float64) string {
	return fmt.Sprintf(
		`{"model":"typesafe/jev-1.13-20260917","answers":{"answer":`+
			`{"type":"choice","choice":%q,"probabilities":{%q:%v},"confidence":%v}},`+
			`"usage":{"input_tokens":300,"output_tokens":40,"cost":0.00001}}`,
		key, key, confidence, confidence)
}

// textServer stands in for the ordinary model. It answers every call with the
// same completion and counts how many times it was asked.
func textServer(t *testing.T, content string, calls *int32) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	return httptest.NewServer(http.HandlerFunc(
		func(writer http.ResponseWriter, _ *http.Request) {
			mu.Lock()
			*calls++
			mu.Unlock()
			writer.Header().Set("Content-Type", "application/json")
			body, _ := json.Marshal(map[string]any{
				"choices": []map[string]any{
					{"message": map[string]any{"content": content}},
				},
			})
			_, _ = writer.Write(body)
		}))
}

// deciding wires a controller to both stand-ins.
func deciding(t *testing.T, decisions, text *httptest.Server, minimum float64) *Controller {
	t.Helper()
	settings := config.Default()
	settings.Matcher = config.Matcher{Enabled: true}
	settings.Model = config.Model{Base: text.URL, Model: "gemma3n:e2b", TimeoutS: 5}
	settings.Decisions = config.Decisions{
		Enabled: true, Base: decisions.URL, Model: "~typesafe/jev-latest",
		MinConfidence: minimum, TimeoutS: 5,
	}
	return New(settings, i18n.Load("id"))
}

var micOnly = []CommandOption{{
	ID:      "mute_mic",
	Phrases: []string{"matikan mikrofon", "matikan mic"},
}}

var withTitle = []CommandOption{
	{ID: "mute_mic", Phrases: []string{"matikan mikrofon"}},
	{
		ID:       "set_title",
		Phrases:  []string{"ganti judul jadi {text}"},
		Slots:    []string{"text"},
		Required: []string{"text"},
	},
}

// A command that takes nothing is finished by the decision model alone. This
// is the whole point of the fast path: most commands have no slot, and for
// those the text model is never asked anything at all.
func TestAConfidentChoiceWithNoSlotsNeverAsksTheTextModel(t *testing.T) {
	seen := &seenDecision{}
	decisions := decisionServer(t, chosen("mute_mic", 0.96), seen)
	defer decisions.Close()

	var textCalls int32
	text := textServer(t, `{"command":"","slots":{}}`, &textCalls)
	defer text.Close()

	guess, err := deciding(t, decisions, text, 0.7).MatchCommand(
		context.Background(), "matiin mic dong", micOnly)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if guess.Command != "mute_mic" {
		t.Errorf("command is %q, want mute_mic", guess.Command)
	}
	if textCalls != 0 {
		t.Errorf("asked the text model %d times; a slotless command must not need it", textCalls)
	}
}

// The request has to be the decisions protocol, not the completions one: its
// own path, the transcript as state, and the options as criteria.
func TestTheDecisionRequestIsTheDecisionsProtocol(t *testing.T) {
	seen := &seenDecision{}
	decisions := decisionServer(t, chosen("mute_mic", 0.96), seen)
	defer decisions.Close()

	var textCalls int32
	text := textServer(t, "", &textCalls)
	defer text.Close()

	_, _ = deciding(t, decisions, text, 0.7).MatchCommand(
		context.Background(), "matiin mic dong", micOnly)

	got := seen.snapshot()
	if got.path != "/decisions" {
		t.Errorf("path is %q, want /decisions", got.path)
	}
	if got.model != "~typesafe/jev-latest" {
		t.Errorf("model is %q", got.model)
	}
	if got.kind != "choice" {
		t.Errorf("question type is %q, want choice", got.kind)
	}
	if !strings.Contains(got.state, "matiin mic dong") {
		t.Errorf("state does not carry the transcript: %q", got.state)
	}
	// The words came from speech recognition, and a model told they were
	// written text rules out the reading that was actually meant.
	if !strings.Contains(strings.ToLower(got.state), "speech") {
		t.Errorf("state does not say the words were heard rather than typed: %q", got.state)
	}
	if _, offered := got.criteria["mute_mic"]; !offered {
		t.Errorf("criteria %v does not offer the command", got.criteria)
	}
}

// Refusing has to be an option it can pick rather than something inferred, or
// a model with no way out distributes its doubt over the real commands.
func TestRefusingIsAlwaysOfferedAsAnOption(t *testing.T) {
	seen := &seenDecision{}
	decisions := decisionServer(t, chosen(NoChoice, 0.98), seen)
	defer decisions.Close()

	var textCalls int32
	text := textServer(t, `{"command":"","slots":{}}`, &textCalls)
	defer text.Close()

	_, _ = deciding(t, decisions, text, 0.7).MatchCommand(
		context.Background(), "hmm apa ya", micOnly)

	if _, offered := seen.snapshot().criteria[NoChoice]; !offered {
		t.Errorf("criteria %v leaves it no way to decline", seen.snapshot().criteria)
	}
}

// Below the threshold nothing is decided here. The text matcher still gets its
// turn, so an unsure decision costs a slower command rather than a missed one.
func TestAnUnsureChoiceFallsThroughToTheTextMatcher(t *testing.T) {
	seen := &seenDecision{}
	// "stop" really does come back like this: the right command, not sure.
	decisions := decisionServer(t, chosen("stop_stream", 0.51), seen)
	defer decisions.Close()

	var textCalls int32
	text := textServer(t, `{"command":"mute_mic","slots":{}}`, &textCalls)
	defer text.Close()

	guess, err := deciding(t, decisions, text, 0.7).MatchCommand(
		context.Background(), "stop", micOnly)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if textCalls == 0 {
		t.Fatal("an unsure decision must still reach the text matcher")
	}
	if guess.Command != "mute_mic" {
		t.Errorf("command is %q; the text matcher's answer should stand", guess.Command)
	}
}

// The failure this is most careful to avoid. A command just under the bar must
// not run, however plausible it looked.
func TestAChoiceUnderTheThresholdDoesNotRunTheCommand(t *testing.T) {
	seen := &seenDecision{}
	decisions := decisionServer(t, chosen("stop_stream", 0.69), seen)
	defer decisions.Close()

	var textCalls int32
	// The text matcher declines too, so nothing is left to carry the command.
	text := textServer(t, `{"command":"","slots":{}}`, &textCalls)
	defer text.Close()

	guess, err := deciding(t, decisions, text, 0.7).MatchCommand(
		context.Background(), "stop", []CommandOption{{ID: "stop_stream"}})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if guess.Command != "" {
		t.Errorf("ran %q at 0.69 against a threshold of 0.70", guess.Command)
	}
}

// A confident decline is still only a decline. It must not be mistaken for a
// command, and the text matcher stays free to find one the descriptions missed.
func TestADeclineIsNotACommand(t *testing.T) {
	seen := &seenDecision{}
	decisions := decisionServer(t, chosen(NoChoice, 0.99), seen)
	defer decisions.Close()

	var textCalls int32
	text := textServer(t, `{"command":"","slots":{}}`, &textCalls)
	defer text.Close()

	guess, err := deciding(t, decisions, text, 0.7).MatchCommand(
		context.Background(), "hmm apa ya enaknya", micOnly)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if guess.Command != "" {
		t.Errorf("command is %q, want none", guess.Command)
	}
}

// It chooses; it does not write. A command that takes a slot needs the text
// model for the words, narrowed to the command already chosen.
func TestASlottedCommandGetsItsSlotFromTheTextModel(t *testing.T) {
	seen := &seenDecision{}
	decisions := decisionServer(t, chosen("set_title", 1.0), seen)
	defer decisions.Close()

	var textCalls int32
	text := textServer(t, `{"command":"set_title","slots":{"text":"main minecraft bareng"}}`, &textCalls)
	defer text.Close()

	guess, err := deciding(t, decisions, text, 0.7).MatchCommand(
		context.Background(), "ganti judul jadi main minecraft bareng", withTitle)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if guess.Command != "set_title" {
		t.Fatalf("command is %q", guess.Command)
	}
	if guess.Slots["text"] != "main minecraft bareng" {
		t.Errorf("text slot is %q; the decision model cannot supply it alone", guess.Slots["text"])
	}
	if textCalls != 1 {
		t.Errorf("asked the text model %d times, want exactly one", textCalls)
	}
}

// If the slot cannot be extracted, the command must not run half-formed.
// Running set_title with no title is worse than not understanding at all.
func TestASlottedCommandWithNoSlotDoesNotRunOnTheDecisionAlone(t *testing.T) {
	seen := &seenDecision{}
	decisions := decisionServer(t, chosen("set_title", 1.0), seen)
	defer decisions.Close()

	var textCalls int32
	text := textServer(t, `{"command":"","slots":{}}`, &textCalls)
	defer text.Close()

	guess, err := deciding(t, decisions, text, 0.7).MatchCommand(
		context.Background(), "ganti judul jadi", withTitle)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if guess.Command == "set_title" && guess.Slots["text"] == "" {
		t.Error("ran set_title with no title")
	}
}

// A provider that invents an option is the backstop case. The answer is
// constrained by the protocol, but nothing that was not offered may reach a
// handler regardless.
func TestACommandThatWasNeverOfferedIsIgnored(t *testing.T) {
	seen := &seenDecision{}
	decisions := decisionServer(t, chosen("format_hard_drive", 1.0), seen)
	defer decisions.Close()

	var textCalls int32
	text := textServer(t, `{"command":"","slots":{}}`, &textCalls)
	defer text.Close()

	guess, err := deciding(t, decisions, text, 0.7).MatchCommand(
		context.Background(), "matiin mic dong", micOnly)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if guess.Command != "" {
		t.Errorf("command is %q; it was never on the list", guess.Command)
	}
}

// A decision provider that is down must cost a slower command, never a command
// that does not run. This is the case that decides whether adding it was safe.
func TestAnUnreachableDecisionProviderFallsBackRatherThanFailing(t *testing.T) {
	broken := httptest.NewServer(http.HandlerFunc(
		func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(writer, `{"error":{"message":"upstream is having a day","code":500}}`)
		}))
	defer broken.Close()

	var textCalls int32
	text := textServer(t, `{"command":"mute_mic","slots":{}}`, &textCalls)
	defer text.Close()

	settings := config.Default()
	settings.Matcher = config.Matcher{Enabled: true}
	settings.Model = config.Model{Base: text.URL, Model: "gemma3n:e2b", TimeoutS: 5}
	settings.Decisions = config.Decisions{
		Enabled: true, Base: broken.URL, Model: "~typesafe/jev-latest",
		MinConfidence: 0.7, TimeoutS: 5,
	}

	guess, err := New(settings, i18n.Load("id")).MatchCommand(
		context.Background(), "matiin mic dong", micOnly)

	if err != nil {
		t.Fatalf("a broken decision provider must not fail the match: %v", err)
	}
	if guess.Command != "mute_mic" {
		t.Errorf("command is %q; the text matcher should have answered", guess.Command)
	}
}

// Leaving [decisions] alone has to leave everything exactly as it was, which
// is what keeps a local-only install a supported way to run this.
func TestWithoutADecisionProviderNothingChanges(t *testing.T) {
	var textCalls int32
	text := textServer(t, `{"command":"mute_mic","slots":{}}`, &textCalls)
	defer text.Close()

	settings := config.Default() // Decisions is off by default
	settings.Matcher = config.Matcher{Enabled: true}
	settings.Model = config.Model{Base: text.URL, Model: "gemma3n:e2b", TimeoutS: 5}

	client := New(settings, i18n.Load("id"))
	if client.DecisionsEndpoint().Configured() {
		t.Fatal("the default config must not resolve a decision endpoint")
	}

	guess, err := client.MatchCommand(context.Background(), "matiin mic dong", micOnly)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if guess.Command != "mute_mic" {
		t.Errorf("command is %q", guess.Command)
	}
	if textCalls == 0 {
		t.Error("the text matcher must still be the one doing the work")
	}
}

// Filling the section in but leaving it switched off must also change nothing,
// so the default address and model can sit in config.toml unused.
func TestTheSectionCanBeFilledInButSwitchedOff(t *testing.T) {
	settings := config.Default()
	settings.Decisions.Base = "https://openrouter.ai/api/alpha"
	settings.Decisions.Model = "~typesafe/jev-latest"
	settings.Decisions.Enabled = false

	if New(settings, i18n.Load("id")).DecisionsEndpoint().Configured() {
		t.Error("a disabled decision endpoint still resolves")
	}
}

// -- the threshold itself --------------------------------------------------

func TestChoseHonoursTheThreshold(t *testing.T) {
	for _, test := range []struct {
		name       string
		key        string
		confidence float64
		minimum    float64
		want       bool
	}{
		{"clear match", "mute_mic", 0.96, 0.7, true},
		{"exactly at the bar", "mute_mic", 0.70, 0.7, true},
		{"just under", "mute_mic", 0.69, 0.7, false},
		{"a confident decline is still a decline", NoChoice, 0.99, 0.7, false},
		{"nothing at all", "", 1.0, 0.7, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			choice := Choice{Key: test.key, Confidence: test.confidence}
			if got := choice.Chose(test.minimum); got != test.want {
				t.Errorf("Chose(%v) is %v, want %v", test.minimum, got, test.want)
			}
		})
	}
}
