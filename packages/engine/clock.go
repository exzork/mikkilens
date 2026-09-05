package engine

import (
	"time"

	"github.com/exzork/mikkilens/packages/audio/feedback"
	"github.com/exzork/mikkilens/packages/core/i18n"
	"github.com/exzork/mikkilens/packages/core/intent"
)

// The time, which is a harder thing to find out mid-stream than it sounds.
//
// The clock is on screen, but reaching it means leaving whatever is in front of
// her, and a stream runs to a schedule: how long until the guest arrives, how
// long this segment has gone on, whether it is late enough to wrap up. Asking
// out loud and being told is one step where the alternative is several.
//
// It is also the cheapest possible command -- no network, no service, nothing
// to be disconnected from -- so it is one of the few that cannot fail. That
// makes it genuinely useful for a different reason: if MikkiLens answers this
// and nothing else, the microphone, recognition, matching and speech are all
// working, and the problem is further out.

// now is the clock, as a variable so tests are not at the mercy of when they
// run. Nothing else should replace it.
var now = time.Now

func clockHandlers(e *Engine) map[string]intent.Handler {
	return map[string]intent.Handler{
		"current_time": e.currentTime,
	}
}

// currentTime says the time on this machine.
//
// Twenty-four hour, because it is what the config, the schedule and the
// broadcast tools all use, and because "14:35" cannot be the wrong half of the
// day the way "2:35" can. The neural voices read that shape as a time rather
// than as a number.
func (e *Engine) currentTime(map[string]string) error {
	e.bus.SayKey("clock.now", feedback.Result,
		i18n.Args{"time": now().Format("15:04")})
	return nil
}
