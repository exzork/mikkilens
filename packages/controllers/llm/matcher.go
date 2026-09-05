package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Understanding a command that fuzzy matching could not.
//
// String similarity is exact about the wrong thing. It compares letters, so
// "matiin mic dong" scores well against "matikan mikrofon" and "tolong jangan
// bacakan chatnya dulu" scores near zero against "jeda chat" -- even though a
// person hears the second pair as obviously the same request. The usual fix is
// to keep adding misheard phrasings to commands.id.toml, which works but
// requires noticing the failure and editing a file, neither of which she can
// do mid-stream.
//
// So this is asked only where the old answer was "I do not know that command":
// nothing that already works gets slower, and the fallback costs a second in
// exchange for a command that would otherwise have done nothing at all.
//
// It is deliberately allowed to refuse. A model that must choose will always
// choose something, and a confident wrong guess is worse here than an honest
// "I did not understand" -- these commands end broadcasts and change what her
// viewers see.

// MatchLimit caps the reply. A command id and a short slot value need very
// few tokens, and a cap keeps a confused model from monologuing.
const MatchLimit = 120

// matchTimeout is how long to wait before giving up and saying the command was
// not understood.
//
// Generous, because the endpoint may well be a small model running on the same
// machine as OBS and a live encode. Past this she is better served by a plain
// "I did not understand" than by more silence.
const matchTimeout = 12 * time.Second

// CommandOption is one command the model may choose, described by its id and
// the phrases already written for it.
type CommandOption struct {
	ID      string
	Phrases []string
	Slots   []string
	// Required are the slots every phrasing of this command takes, so the
	// schema can insist on them without pushing the model into inventing one
	// for a phrasing that does not need it.
	Required []string
}

// CommandGuess is what the model made of an utterance.
type CommandGuess struct {
	Command string
	Slots   map[string]string
}

// MatchCommand asks the model which command an utterance meant.
//
// An empty command means it did not recognise one, which is a valid and
// expected answer rather than a failure.
func (c *Controller) MatchCommand(
	ctx context.Context, transcript string, options []CommandOption,
) (CommandGuess, error) {
	if strings.TrimSpace(transcript) == "" || len(options) == 0 {
		return CommandGuess{}, nil
	}

	endpoint := c.MatcherEndpoint()
	if !endpoint.Configured() {
		return CommandGuess{}, &Error{Reason: "no command matcher is configured"}
	}

	timed, cancel := context.WithTimeout(ctx, matchTimeout)
	defer cancel()

	// Tools first, because the provider then constrains the answer to a command
	// that exists and slots that were declared. The prompt below is the
	// fallback for endpoints that cannot do it.
	if toolsSupported(endpoint) {
		guess, err := c.matchWithTools(timed, transcript, options, endpoint)
		if err == nil {
			return guess, nil
		}
		if !unsupportedTools(err) {
			return CommandGuess{}, err
		}
		rememberToolsUnsupported(endpoint)
	}

	answer, err := c.Complete(timed, []Message{
		{Role: "system", Content: matchSystemPrompt(options)},
		{Role: "user", Content: transcript},
	}, endpoint, MatchLimit)
	if err != nil {
		return CommandGuess{}, err
	}
	return parseGuess(answer), nil
}

// MatcherEndpoint is the same provider as everything else, with a timeout of
// its own.
//
// The timeout is the only difference worth having. A summary she asked for can
// take its time; this one is in the way of a command she has already spoken,
// and past a few seconds silence is worse than an honest "I did not
// understand".
//
// Switching [matcher] off returns an unconfigured endpoint rather than a
// working one, so unrecognised speech is never sent anywhere.
func (c *Controller) MatcherEndpoint() Endpoint {
	if !c.settings.Matcher.Enabled {
		return Endpoint{}
	}
	base, model, key := c.settings.ModelEndpoint()
	return Endpoint{BaseURL: base, Model: model, APIKey: key, Timeout: matchTimeout}
}

// matchSystemPrompt describes the commands using the phrases already written
// for them, so the file she edits stays the single source of truth.
func matchSystemPrompt(options []CommandOption) string {
	var builder strings.Builder
	builder.WriteString(
		"You map a voice command, transcribed by speech recognition and " +
			"possibly misheard, onto one of the commands below.\n\n" +
			"Commands:\n")

	for _, option := range options {
		fmt.Fprintf(&builder, "- %s", option.ID)
		if len(option.Phrases) > 0 {
			fmt.Fprintf(&builder, ": %s", strings.Join(option.Phrases, "; "))
		}
		if len(option.Slots) > 0 {
			fmt.Fprintf(&builder, " [takes: %s]", strings.Join(option.Slots, ", "))
		}
		builder.WriteString("\n")
	}

	builder.WriteString(
		"\nReply with JSON only, no explanation and no code fence:\n" +
			`{"command": "<id>", "slots": {}}` + "\n\n" +
			"Rules:\n" +
			"- The id must be exactly one from the list above.\n" +
			"- If a command takes a slot, put the spoken value in slots, " +
			"for example {\"text\": \"main minecraft\"}.\n" +
			"- The speech may be misheard, so judge it by what it sounds like " +
			"it was meant to be, not by exact spelling.\n" +
			`- If none of them is clearly what was meant, reply {"command": ""}. ` +
			"Refusing is correct and expected. Never guess between two " +
			"plausible commands: these start and stop live broadcasts, and " +
			"doing the wrong one is far worse than doing nothing.")

	return builder.String()
}

// parseGuess reads the model's answer, tolerating the wrappers small models
// tend to add: a code fence, a sentence before the JSON, a trailing full stop.
func parseGuess(answer string) CommandGuess {
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return CommandGuess{}
	}

	start := strings.Index(answer, "{")
	end := strings.LastIndex(answer, "}")
	if start < 0 || end <= start {
		return CommandGuess{}
	}

	var parsed struct {
		Command string            `json:"command"`
		Slots   map[string]string `json:"slots"`
	}
	if err := json.Unmarshal([]byte(answer[start:end+1]), &parsed); err != nil {
		return CommandGuess{}
	}

	guess := CommandGuess{
		Command: strings.TrimSpace(parsed.Command),
		Slots:   map[string]string{},
	}
	for name, value := range parsed.Slots {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			guess.Slots[strings.TrimSpace(name)] = trimmed
		}
	}
	return guess
}

// -- tools ---------------------------------------------------------------

// Offering the commands as tools rather than as a list in a prompt.
//
// Both describe the same commands, and the difference is whose job it is to
// keep the answer well formed. A prompt asks for JSON and hopes: the reply
// arrives wrapped in a code fence, or with a sentence in front of it, or
// naming a command that does not exist, and parseGuess below exists entirely
// to cope with that. A tool call is constrained by the provider against a
// schema, so the name is always one that was offered and the arguments are
// always the slots that were declared.
//
// Not every endpoint supports them. Small local servers are exactly the ones
// most likely to refuse, and are exactly what this is usually pointed at, so
// the prompt path stays as the fallback and an endpoint that rejects tools is
// remembered rather than retried on every utterance -- this sits in the way of
// a command she has already spoken.

// slotDescriptions say what each known slot holds.
//
// The slot names come from commands.toml and are terse by design. "scene" tells
// a model very little on its own; "the name of the OBS scene to switch to"
// tells it what to extract and, as importantly, what not to.
var slotDescriptions = map[string]string{
	"scene":    "The name of the OBS scene, as she said it.",
	"source":   "The name of the source in the current scene, as she said it.",
	"text":     "The text to use, exactly as she said it, with nothing added.",
	"question": "The question she asked, in full.",
	"value":    "The value she gave, as she said it.",
	"channel":  "The name of the channel, as she said it.",
}

// toolsFor turns the commands into tool definitions.
func toolsFor(options []CommandOption) []Tool {
	tools := make([]Tool, 0, len(options))
	for _, option := range options {
		properties := map[string]ToolProperty{}
		for _, slot := range option.Slots {
			description := slotDescriptions[slot]
			if description == "" {
				description = "The " + slot + " she named."
			}
			properties[slot] = ToolProperty{Type: "string", Description: description}
		}

		tools = append(tools, Tool{
			Type: "function",
			Function: ToolFunction{
				Name:        option.ID,
				Description: toolDescription(option),
				Parameters: ToolSchema{
					Type:                 "object",
					Properties:           properties,
					Required:             option.Required,
					AdditionalProperties: false,
				},
			},
		})
	}
	return tools
}

// toolDescription is the phrases she actually uses for a command.
//
// They are worth far more than the id: "chat_skip_to_now" says almost nothing,
// while "susul chat; lompat ke chat terbaru" says exactly when to call it, in
// her own words and her own language.
func toolDescription(option CommandOption) string {
	if len(option.Phrases) == 0 {
		return "The " + option.ID + " command."
	}
	return "Call this when she says something like: " + strings.Join(option.Phrases, "; ")
}

// toolSystemPrompt is short because the tools carry the descriptions.
//
// What is left is the part no schema can express: that the transcript may be
// misheard, and that calling nothing is a real answer.
const toolSystemPrompt = "You turn a voice command into one tool call. The " +
	"words come from speech recognition and may be misheard, so judge them by " +
	"what they sound like they were meant to be rather than by exact spelling.\n\n" +
	"Call exactly one tool, or none at all. Calling nothing is correct and " +
	"expected whenever no tool is clearly what she meant. Never choose between " +
	"two plausible tools: these start and stop live broadcasts, and doing the " +
	"wrong one is far worse than doing nothing."

// matchWithTools asks with the commands on the table as tools.
func (c *Controller) matchWithTools(
	ctx context.Context, transcript string, options []CommandOption, endpoint Endpoint,
) (CommandGuess, error) {
	content, calls, err := c.CompleteTools(ctx, []Message{
		{Role: "system", Content: toolSystemPrompt},
		{Role: "user", Content: transcript},
	}, endpoint, MatchLimit, toolsFor(options))
	if err != nil {
		return CommandGuess{}, err
	}

	// More than one call is a model that has not understood the question. Doing
	// the first of several commands she did not ask for is the failure this is
	// most careful to avoid, so it counts as no answer.
	if len(calls) > 1 {
		return CommandGuess{}, nil
	}
	if len(calls) == 1 {
		return guessFromCall(calls[0], options), nil
	}

	// No call. Usually a refusal, which is the answer wanted. But a server that
	// ignored the tools entirely and answered in prose looks identical from
	// here, so anything it said is given to the old parser before giving up.
	return parseGuess(content), nil
}

// guessFromCall keeps only what was actually offered.
//
// The provider is supposed to constrain both, and mostly does. This is the
// backstop for the ones that do not: a tool name that was never offered, or an
// argument that is not a slot of the command it was given to, means the model
// has invented something, and inventing is the one thing that must not reach a
// handler.
func guessFromCall(call ToolCall, options []CommandOption) CommandGuess {
	for _, option := range options {
		if option.ID != call.Name {
			continue
		}
		allowed := map[string]bool{}
		for _, slot := range option.Slots {
			allowed[slot] = true
		}
		slots := map[string]string{}
		for name, value := range call.Arguments {
			if allowed[name] {
				slots[name] = value
			}
		}
		return CommandGuess{Command: option.ID, Slots: slots}
	}
	return CommandGuess{}
}

// Endpoints that turned out not to support tools.
//
// Keyed by base URL and model, because that pair is what decides it. Remembered
// for the life of the process: this is in the way of a spoken command, and
// paying a failed round trip before every fallback would add a second to every
// command the phrases did not match.
var toolless sync.Map

func toolKey(endpoint Endpoint) string { return endpoint.BaseURL + "\x00" + endpoint.Model }

func toolsSupported(endpoint Endpoint) bool {
	_, refused := toolless.Load(toolKey(endpoint))
	return !refused
}

func rememberToolsUnsupported(endpoint Endpoint) {
	toolless.Store(toolKey(endpoint), true)
}

// unsupportedTools reports whether an error is the endpoint saying it does not
// do tool calling, rather than something that would fail the same way again.
//
// Matched on the message because there is no status code for it: providers
// answer 400, 404, 422 or 500 for the same complaint, and the only thing they
// agree on is naming the field.
func unsupportedTools(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	if !strings.Contains(message, "tool") && !strings.Contains(message, "function") {
		return false
	}
	for _, phrase := range []string{
		"not support", "unsupported", "unrecognized", "unrecognised",
		"unknown", "unexpected", "invalid", "no such", "cannot be used",
	} {
		if strings.Contains(message, phrase) {
			return true
		}
	}
	return false
}

// -- answering from a result ----------------------------------------------

// answerTimeout is the whole second pass: running the command took no time,
// and what is left is one short reply. Longer than the matcher's, because this
// one is generating sentences rather than picking a name, and shorter than a
// summary's, because she is standing there waiting for it.
const answerTimeout = 20 * time.Second

// AnswerLimit caps the reply. Two or three sentences read aloud is already
// more than most of these questions need.
const AnswerLimit = 220

// AnswerFromResult turns a command's result into an answer to what she asked.
//
// The command has already run. What comes back from it is a sentence in her
// language -- "Sekarang jam 14:35." -- and the question was something that
// sentence does not directly answer: how long until noon, whether there is
// time for one more game, how late it is getting.
//
// Sentences are handed over as they arrive rather than at the end, so the
// answer starts while the rest is still being written.
func (c *Controller) AnswerFromResult(
	ctx context.Context, transcript, command, result string, onSentence func(string),
) (string, error) {
	endpoint := c.MatcherEndpoint()
	if !endpoint.Configured() {
		return "", &Error{Reason: "no model endpoint is configured"}
	}
	endpoint.Timeout = answerTimeout

	timed, cancel := context.WithTimeout(ctx, answerTimeout)
	defer cancel()

	return c.CompleteStream(timed, []Message{
		{Role: "system", Content: answerSystemPrompt(c.LanguageInstruction())},
		{Role: "user", Content: transcript},
		{Role: "assistant", Content: fmt.Sprintf("I used %s and it reported: %s", command, result)},
		{Role: "user", Content: "Now answer my question using that."},
	}, endpoint, AnswerLimit, onSentence)
}

// answerSystemPrompt is what separates answering from repeating.
//
// The failure worth guarding against is not a wrong answer but a padded one.
// Every word is read aloud at speaking speed, so a preamble is a second of her
// time, and a model that explains its working before answering has buried the
// answer under the part she did not ask for.
func answerSystemPrompt(language string) string {
	return "You answer a question using a result MikkiLens has already looked " +
		"up for her. Use it as fact: it is correct and it is current.\n\n" +
		"Answer in one short sentence, and put the answer first. No preamble, " +
		"no restating the question, no explaining how you worked it out, no " +
		"offering to help further. If the result does not contain what is " +
		"needed to answer, say so plainly in one sentence instead of " +
		"guessing.\n\n" + language
}
