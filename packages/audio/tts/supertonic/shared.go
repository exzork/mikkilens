package supertonic

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// The engine is a process-wide singleton because of what it costs: four
// hundred megabytes of weights and most of a second to load them. Opening one
// per utterance would make every phrase slower than the online voice it
// replaced, which is the opposite of the point.
//
// It is opened lazily rather than at startup. Somebody using the Edge voices
// should not be paying for this in memory, and somebody who has not downloaded
// the models should not be told about it until they ask for it.
var (
	sharedMu     sync.Mutex
	shared       *Engine
	sharedFailed error
	sharedAt     time.Time
)

// retryAfter is how long a failure to load is believed for.
//
// Without it, a missing file or a half-finished download turns every single
// utterance into another attempt to load four hundred megabytes, and she hears
// the delay on every one of them. With it, the first failure is fast and the
// rest are instant until something has plausibly changed -- a download
// finishing, most likely.
const retryAfter = 30 * time.Second

// Shared returns the loaded engine, opening it the first time.
//
// The load happens with the caller's context, so the first phrase after a
// cold start can be cut off like any other rather than blocking the speech
// bus for a second.
func Shared(ctx context.Context) (*Engine, error) {
	sharedMu.Lock()
	defer sharedMu.Unlock()

	if shared != nil {
		return shared, nil
	}
	if sharedFailed != nil && time.Since(sharedAt) < retryAfter {
		return nil, sharedFailed
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	started := time.Now()
	engine, err := Open()
	sharedAt = time.Now()
	if err != nil {
		sharedFailed = err
		return nil, err
	}
	sharedFailed = nil
	shared = engine
	slog.Info("the local voice is ready", "took", time.Since(started).Round(time.Millisecond))
	return shared, nil
}

// Ready reports whether the engine is already loaded, without loading it.
//
// The status page asks, so "the voice is warming up" can be said once rather
// than discovered as a pause in the middle of a sentence.
func Ready() bool {
	sharedMu.Lock()
	defer sharedMu.Unlock()
	return shared != nil
}

// Release unloads the engine and gives the memory back.
//
// Switching to the online voice calls it: four hundred megabytes held open for
// something no longer being used is four hundred megabytes the game does not
// have, on a machine that is also encoding video.
func Release() {
	sharedMu.Lock()
	engine := shared
	shared, sharedFailed = nil, nil
	sharedMu.Unlock()

	if engine != nil {
		engine.Close()
		slog.Info("the local voice was unloaded")
	}
}
