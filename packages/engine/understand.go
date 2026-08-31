package engine

import (
	"context"
	"log/slog"

	"github.com/exzork/mikkilens/packages/audio/feedback"
	"github.com/exzork/mikkilens/packages/core/i18n"

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

// matcherConfigured reports whether a fallback is available at all: either the
// model MikkiLens runs itself, or an endpoint she pointed it at.
func matcherConfigured(settings config.Config) bool {
	if !settings.Matcher.Enabled {
		return false
	}
	base, model, _ := settings.MatcherEndpoint()
	if base != "" && model != "" {
		return true
	}
	// The bundled server counts as soon as there is something to run: the
	// router asks it only on a miss, and by then it will have finished
	// loading or be reported as unavailable.
	return llm.RuntimeInstalled() && llm.InstalledModel() != ""
}

// startBundledModel loads the local model in the background.
//
// At startup rather than on the first miss: loading gigabytes from disk takes
// long enough that a command would appear to be ignored, and being ignored is
// the failure she cannot tell apart from a broken microphone.
func (e *Engine) startBundledModel(settings config.Config) {
	if !settings.Matcher.Enabled {
		return
	}
	if base, _, _ := settings.MatcherEndpoint(); base != "" {
		return // she pointed it somewhere else
	}
	if !llm.RuntimeInstalled() || llm.InstalledModel() == "" {
		return // nothing downloaded yet, which is the state it ships in
	}

	go func() {
		if err := llm.Bundled().Start(context.Background()); err != nil {
			slog.Info("the local language model is not available", "reason", err)
		}
	}()
}

// applyUnderstander installs or removes the fallback to match the settings.
func (e *Engine) applyUnderstander(settings config.Config) {
	if matcherConfigured(settings) {
		e.router.SetUnderstander(&understander{engine: e})
		return
	}
	e.router.SetUnderstander(nil)
}

// OnMatcherProgress reports a model download aloud.
//
// A three gigabyte download with no feedback is indistinguishable from one
// that has died, and she cannot glance at a progress bar to check. So it is
// spoken -- but only at quarters, because a number read out every half second
// would make the machine unusable for the hour it takes.
func (e *Engine) OnMatcherProgress(progress llm.Progress) {
	switch progress.Stage {
	case "done":
		e.bus.SayKey("matcher.downloaded", feedback.Result)
		// Load it now rather than at the next start: she asked for this and
		// should be able to use it in the same session.
		e.startBundledModel(e.Config())
		return
	case "error":
		slog.Warn("the model download failed", "error", progress.Detail)
		e.bus.SayKey("matcher.download_failed", feedback.Error,
			i18n.Args{"reason": llm.Readable(progress.Detail)})
		return
	case "runtime":
		return // small and quick; not worth narrating
	}

	quarter := progress.Percent / 25
	e.mu.Lock()
	announce := quarter > e.matcherQuarter && quarter > 0
	if announce {
		e.matcherQuarter = quarter
	}
	e.mu.Unlock()

	if announce {
		e.bus.SayKey("matcher.downloading", feedback.Result,
			i18n.Args{"percent": quarter * 25})
	}
}
