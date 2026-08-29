// Package audio holds the one switch that mutes MikkiLens without changing
// anything else about how it behaves.
package audio

import (
	"os"
	"strings"
	"sync/atomic"
)

// MIKKILENS_SILENT=1 suppresses actual sound output while leaving every other
// code path intact: synthesis still runs, the queue still orders and preempts,
// callbacks still fire. That makes it useful both for running MikkiLens while
// the machine is busy streaming, and for tests on a machine with no audio
// hardware at all.

var override atomic.Int32 // 0 unset, 1 silent, 2 audible

var silentValues = map[string]bool{"1": true, "true": true, "yes": true, "on": true}

// Silent reports whether audio output is suppressed for this process.
func Silent() bool {
	switch override.Load() {
	case 1:
		return true
	case 2:
		return false
	}
	return silentValues[strings.ToLower(strings.TrimSpace(os.Getenv("MIKKILENS_SILENT")))]
}

// SetSilent turns output off or back on for the rest of the run.
func SetSilent(silent bool) {
	if silent {
		override.Store(1)
		return
	}
	override.Store(2)
}
