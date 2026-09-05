package engine

import (
	"context"
	"log/slog"

	"github.com/exzork/mikkilens/packages/audio/feedback"

	"github.com/exzork/mikkilens/packages/controllers/llm"
	"github.com/exzork/mikkilens/packages/core/config"
	"github.com/exzork/mikkilens/packages/core/intent"
)

// The bridge between the router and the model.
//
// The router asks "what did she mean by this"; the model client knows how to
// ask a provider. Neither should know about the other, so the translation
// between them lives here: the router stays testable without a network, and
// the client stays a plain provider-agnostic thing that knows nothing about
// commands.

// understander turns unmatched speech into a command using a small model.
type understander struct {
	engine *Engine
}

// Understand implements intent.Understander.
func (u *understander) Understand(
	ctx context.Context, transcript string, commands *intent.Set,
) (intent.Resolution, error) {
	settings := u.engine.Config()
	client := llm.New(settings, u.engine.Locale())

	guess, err := client.MatchCommand(ctx, transcript, commandOptions(commands))
	if err != nil {
		return intent.Resolution{}, err
	}
	if guess.Command == "" {
		return intent.Resolution{}, nil
	}

	// A command that reports rather than acts, reached by a question the
	// phrases did not match, is answered rather than read out. "Berapa menit
	// lagi sampai jam 12" needs the time; it is not a request to be told it.
	if command, known := commands.Commands[guess.Command]; known && command.Answers {
		if spoke, err := u.answer(ctx, client, transcript, guess); err != nil {
			// The command still exists and still works. Falling through to the
			// ordinary path means she hears the time rather than nothing,
			// which is a worse answer to her question but a far better outcome
			// than silence.
			slog.Warn("could not answer from the command result",
				"command", guess.Command, "error", err)
		} else if spoke {
			return intent.Resolution{Answered: true}, nil
		}
	}
	return intent.Resolution{Command: guess.Command, Slots: guess.Slots}, nil
}

// answer runs the command, hands the result back to the model, and speaks what
// it makes of it.
//
// Reports whether anything was said. A report the engine cannot produce, or a
// model that answers with nothing, both mean "carry on as usual" rather than
// "fail": the ordinary path still says the plain result, which answers a
// narrower question than she asked but answers something.
func (u *understander) answer(
	ctx context.Context, client *llm.Controller, transcript string, guess llm.CommandGuess,
) (bool, error) {
	report, found := u.engine.report(guess.Command, guess.Slots)
	if !found {
		return false, nil
	}
	if report == "" {
		return false, nil
	}

	// Spoken a sentence at a time as the model writes them, so the answer
	// starts before it has finished thinking. Silence is the one thing this
	// application treats as a fault.
	spoken := 0
	_, err := client.AnswerFromResult(ctx, transcript, guess.Command, report,
		func(sentence string) {
			spoken++
			u.engine.bus.Say(sentence, feedback.Result)
		})
	if err != nil {
		return spoken > 0, err
	}
	return spoken > 0, nil
}

// commandOptions describes the commands to the model using the phrases already
// written for them, so commands.id.toml stays the one place they are defined.
//
// The phrases are worth sending even though they failed to match: they are how
// she actually talks about each command, which tells a model far more than an
// id like "chat_skip_to_now" does on its own.
func commandOptions(commands *intent.Set) []llm.CommandOption {
	if commands == nil {
		return nil
	}

	options := make([]llm.CommandOption, 0, len(commands.Order))
	for _, id := range commands.Order {
		command := commands.Commands[id]

		phrases := make([]string, 0, len(command.Phrases))
		slots := map[string]bool{}
		names := []string{}
		counts := map[string]int{}
		for _, phrase := range command.Phrases {
			phrases = append(phrases, phrase.Raw)
			seen := map[string]bool{}
			for _, name := range phrase.SlotNames {
				if !slots[name] {
					slots[name] = true
					names = append(names, name)
				}
				if !seen[name] {
					seen[name] = true
					counts[name]++
				}
			}
		}

		// Required means every way of saying it takes that value. A command
		// with both "stop the stream" and "stop the stream in {value}" can be
		// called with nothing, and marking the slot required would push the
		// model into inventing one.
		required := []string{}
		for _, name := range names {
			if counts[name] == len(command.Phrases) {
				required = append(required, name)
			}
		}

		options = append(options, llm.CommandOption{
			ID: id, Phrases: phrases, Slots: names, Required: required,
		})
	}
	return options
}

// matcherConfigured reports whether the fallback can be asked at all: the
// switch is on, and there is a model to ask.
func matcherConfigured(settings config.Config) bool {
	return settings.Matcher.Enabled && settings.Model.Configured()
}

// applyUnderstander installs or removes the fallback to match the settings.
func (e *Engine) applyUnderstander(settings config.Config) {
	if matcherConfigured(settings) {
		e.router.SetUnderstander(&understander{engine: e})
		return
	}
	e.router.SetUnderstander(nil)
}
