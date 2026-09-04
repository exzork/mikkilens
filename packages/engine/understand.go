package engine

import (
	"context"

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
) (string, map[string]string, error) {
	settings := u.engine.Config()
	client := llm.New(settings, u.engine.Locale())

	guess, err := client.MatchCommand(ctx, transcript, commandOptions(commands))
	if err != nil {
		return "", nil, err
	}
	return guess.Command, guess.Slots, nil
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
		for _, phrase := range command.Phrases {
			phrases = append(phrases, phrase.Raw)
			for _, name := range phrase.SlotNames {
				if !slots[name] {
					slots[name] = true
					names = append(names, name)
				}
			}
		}
		options = append(options, llm.CommandOption{
			ID: id, Phrases: phrases, Slots: names,
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
