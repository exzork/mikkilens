package omnivoice

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// The engine is a process-wide singleton for the same reason Supertonic's is,
// only more so: this is 1.4 GB of weights and several seconds to load them.
//
// It is opened lazily. Somebody using the Edge voices or Supertonic should not
// be paying for this in memory, and on a machine that is also encoding video
// there is no such thing as a gigabyte held just in case.
var (
	sharedMu     sync.Mutex
	shared       *Engine
	sharedFailed error
	sharedAt     time.Time
)

// retryAfter is how long a failure to load is believed for.
//
// Without it, a half-finished download turns every single utterance into
// another attempt to load two gigabytes, and she hears that delay on every one
// of them. With it, the first failure is fast and the rest are instant until
// something has plausibly changed.
const retryAfter = 30 * time.Second

// Shared returns the loaded engine, opening it the first time.
//
// The load happens with the caller's context, so the first phrase after a cold
// start can be cut off like any other rather than blocking the speech bus.
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
	slog.Info("OmniVoice is ready", "took", time.Since(started).Round(time.Millisecond))
	return shared, nil
}

// Ready reports whether the engine is already loaded, without loading it.
func Ready() bool {
	sharedMu.Lock()
	defer sharedMu.Unlock()
	return shared != nil
}

// Release unloads the engine and gives the memory back.
//
// Switching away from OmniVoice calls it. A gigabyte and a half held open for
// something no longer in use is a gigabyte and a half the game does not have.
func Release() {
	sharedMu.Lock()
	engine := shared
	shared, sharedFailed = nil, nil
	sharedMu.Unlock()

	if engine != nil {
		engine.Close()
		slog.Info("OmniVoice was unloaded")
	}
}
