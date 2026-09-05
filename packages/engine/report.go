package engine

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/exzork/mikkilens/packages/audio/feedback"
	"github.com/exzork/mikkilens/packages/controllers/search"
	"github.com/exzork/mikkilens/packages/core/i18n"
	"github.com/exzork/mikkilens/packages/core/intent"
)

// Commands that answer a question, in a form something else can read.
//
// Every other command speaks and is done: the sentence goes on the bus and
// that is the whole of it. That is right when the sentence is the answer, and
// wrong when it is only the ingredient -- "berapa menit lagi sampai jam 12"
// needs the time, but being told the time is not an answer to it.
//
// So these produce their sentence rather than saying it, which lets the caller
// decide whether it is the answer or the input to one. The sentence is the
// same either way: there is one wording per command, in her language, and it
// does not fork depending on who ends up hearing it.
//
// Only commands marked `answers` in commands.toml ever come through here, and
// only through the model's path -- a phrase that matched exactly still runs
// the ordinary handler and is spoken immediately.

// searchTimeout bounds the lookup. The package has its own, shorter deadline;
// this is the backstop.
const searchTimeout = 15 * time.Second

// trimmed is strings.TrimSpace, named for what it is used for here.
func trimmed(value string) string { return strings.TrimSpace(value) }

// Reporter produces what a command has to say, without saying it.
type Reporter func(slots map[string]string) string

func (e *Engine) reporters() map[string]Reporter {
	return map[string]Reporter{
		"current_time": e.reportTime,
		"search_web":   e.reportSearch,
	}
}

// report runs one reporting command. The second return is false when the
// command has no reporter, which is the signal to fall back to speaking it the
// ordinary way.
func (e *Engine) report(command string, slots map[string]string) (string, bool) {
	reporter, known := e.reporters()[command]
	if !known {
		return "", false
	}
	return reporter(slots), true
}

// reportTime is currentTime's sentence, unspoken.
//
// Deliberately the same string the handler speaks. A model given "It is 14:35."
// has everything it needs to work out how long until noon, and giving it a
// second, machine-shaped wording would mean two things to keep in step for no
// gain.
func (e *Engine) reportTime(map[string]string) string {
	return e.Locale().T("clock.now", i18n.Args{"time": now().Format("15:04")})
}

// reportSearch looks the question up and hands back what came out.
//
// The model behind [model] has no live access of its own, so this is how a
// question about anything outside MikkiLens gets an answer at all: MikkiLens
// searches, the model reads the results and says the one sentence worth
// hearing. Neither half is much use without the other -- results read aloud
// are a list of page titles, and the model alone would be guessing.
//
// A failed lookup returns the locale's own sentence rather than an error. The
// model is told to relay it, which keeps "the search could not be reached"
// sounding like MikkiLens rather than like a stack trace.
func (e *Engine) reportSearch(slots map[string]string) string {
	locale := e.Locale()
	question := trimmed(slots["question"])
	if question == "" {
		question = trimmed(slots["text"])
	}
	if question == "" {
		return locale.T("search.nothing_asked")
	}

	ctx, cancel := context.WithTimeout(context.Background(), searchTimeout)
	defer cancel()

	results, err := search.Web(ctx, question)
	if err != nil {
		slog.Warn("the search failed", "question", question, "error", err)
		return locale.T("search.failed")
	}
	readable := search.Readable(results)
	if readable == "" {
		return locale.T("search.nothing_found", i18n.Args{"question": question})
	}
	return readable
}

// searchHandlers is the ordinary path: she said a search phrase outright, so
// there is no question to reason about, and the results are read as they are.
//
// Worth having even though the model usually answers instead. A phrase that
// matched exactly must never depend on a model being reachable -- if the
// endpoint is down, "cari harga bitcoin" still searches and still says
// something back.
func searchHandlers(e *Engine) map[string]intent.Handler {
	return map[string]intent.Handler{
		"search_web": func(slots map[string]string) error {
			e.bus.Say(e.reportSearch(slots), feedback.Result)
			return nil
		},
	}
}
