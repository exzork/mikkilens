package engine

import (
	"log/slog"
	"strings"
	"time"

	"github.com/exzork/mikkilens/packages/audio/feedback"
	"github.com/exzork/mikkilens/packages/controllers/desktop"
	"github.com/exzork/mikkilens/packages/core/i18n"
	"github.com/exzork/mikkilens/packages/core/intent"
	"github.com/exzork/mikkilens/packages/core/state"
)

// Finishing for the day, as one sentence.
//
// The end of a stream is a dozen windows to find and close on a machine she is
// not looking at -- and it is the moment she most wants to be finished. So
// "tutup semua" stops the stream, ends the broadcast and asks every open
// application to close, then says the machine is ready to be switched off.
//
// It does not switch anything off itself. What is left is a machine with
// nothing running and nothing unsaved, so pressing the power button is safe;
// deciding to press it stays hers.

// closeSettle is how long applications are given to go before what is left is
// reported as refusing.
//
// Long enough for a browser with many tabs, short enough that she is not left
// listening to silence wondering whether the command worked. Anything still
// there after this is almost always asking about unsaved work, which is a
// thing to be told about rather than waited out.
const closeSettle = 6 * time.Second

func sessionHandlers(e *Engine) map[string]intent.Handler {
	return map[string]intent.Handler{
		"close_stream": e.closeStream,
		"close_obs":    e.closeOBS,
	}
}

// obsExecutables are the names OBS runs under: the 64-bit build everybody has
// now, and the two older ones.
var obsExecutables = []string{"obs64.exe", "obs32.exe", "obs.exe"}

// obsCloseQuiet is how long after she closes OBS its disconnection goes
// unannounced. "OBS terputus, mencoba menyambung lagi" straight after "OBS
// sudah ditutup" would be MikkiLens contradicting itself.
const obsCloseQuiet = 30 * time.Second

// closeOBS closes OBS alone: the stream stopped and the broadcast ended first
// if she is still live, then OBS asked to close the way the X would.
func (e *Engine) closeOBS(map[string]string) error {
	e.stopBeforeClosing()

	e.mu.Lock()
	e.obsClosedAt = time.Now()
	e.mu.Unlock()

	found, stillOpen, err := desktop.CloseApp(closeSettle, obsExecutables...)
	switch {
	case err != nil:
		slog.Error("could not close OBS", "error", err)
		e.bus.SayKey("session.close_failed", feedback.Error, i18n.Args{"reason": err.Error()})
	case !found:
		e.bus.SayKey("session.obs_not_open", feedback.Result)
	case stillOpen:
		e.bus.SayKey("session.obs_still_open", feedback.Error)
	default:
		e.bus.SayKey("session.obs_closed", feedback.Result)
	}
	return nil
}

// obsClosedOnPurpose reports whether OBS going away is the close she asked for.
func (e *Engine) obsClosedOnPurpose() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return !e.obsClosedAt.IsZero() && time.Since(e.obsClosedAt) < obsCloseQuiet
}

// stopBeforeClosing stops a stream that is still running and ends its
// broadcast. Closing OBS while it is streaming ends the broadcast by pulling
// the cable out, which is what everybody watching would see.
func (e *Engine) stopBeforeClosing() {
	if controller := e.OBS(); controller != nil && controller.Connected() {
		if live, err := controller.IsStreaming(); err == nil && live {
			if err := controller.StopStream(); err != nil {
				slog.Error("could not stop the stream", "error", err)
			} else {
				e.store.Update(state.Changes{"streaming": false})
				e.bus.SayKey("obs.stream_stopped", feedback.Result)
			}
		}
	}
	e.endBroadcast()
}

func (e *Engine) closeStream(map[string]string) error {
	// The stream first; see stopBeforeClosing.
	e.stopBeforeClosing()

	e.mu.Lock()
	e.obsClosedAt = time.Now()
	e.mu.Unlock()

	locale := e.Locale()
	e.bus.SayKey("session.closing", feedback.Result)

	asked, remaining, err := desktop.CloseAll(closeSettle)
	switch {
	case err != nil:
		slog.Error("could not close the open applications", "error", err)
		e.bus.SayKey("session.close_failed", feedback.Error,
			i18n.Args{"reason": err.Error()})
	case len(asked) == 0:
		e.bus.SayKey("session.nothing_open", feedback.Result)
	case len(remaining) == 0:
		e.bus.SayKey("session.ready", feedback.Result)
	default:
		// Named rather than counted: "two still open" leaves her hunting, and
		// the one still open is nearly always the one asking about unsaved
		// work, which she can answer once she knows which it is.
		e.bus.Say(locale.T("session.still_open", i18n.Args{
			"apps": strings.Join(remaining, ", "),
		}), feedback.Result)
	}
	return nil
}
