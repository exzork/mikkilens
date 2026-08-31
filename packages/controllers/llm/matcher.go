package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
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

// matchTimeout is how long to wait for a local model before giving up and
// saying the command was not understood.
//
// Longer than a remote call would need, because this is expected to be a
// small model on the same machine as OBS and a running encode. Past this she
// is better served by a plain "I did not understand" than by more silence.
const matchTimeout = 12 * time.Second

// CommandOption is one command the model may choose, described by its id and
// the phrases already written for it.
type CommandOption struct {
	ID      string
	Phrases []string
	Slots   []string
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

	answer, err := c.Complete(timed, []Message{
		{Role: "system", Content: matchSystemPrompt(options)},
		{Role: "user", Content: transcript},
	}, endpoint, MatchLimit)
	if err != nil {
		return CommandGuess{}, err
	}
	return parseGuess(answer), nil
}

// MatcherEndpoint is the small local model used for fallback matching.
//
// Separate from Endpoint on purpose: that one falls back to the vision
// provider, which for this would mean sending every unrecognised utterance to
// a paid cloud API without anyone asking for that.
func (c *Controller) MatcherEndpoint() Endpoint {
	base, model, key := c.settings.MatcherEndpoint()
	if base == "" && Bundled().BaseURL() != "" {
		// The model MikkiLens runs itself. Any model name will do: the server
		// has exactly one loaded and ignores what it is asked for.
		return Endpoint{
			BaseURL: Bundled().BaseURL(),
			Model:   "local",
			Timeout: matchTimeout,
		}
	}
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
