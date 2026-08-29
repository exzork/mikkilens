// Package earcons renders the short non-speech tones.
//
// Earcons carry the acknowledgement that speech is too slow to give: the
// listening tone fires the instant the hotkey is pressed, roughly a second
// before any synthesized voice could. They have to be told apart by ear alone,
// so each one has a distinct contour rather than a distinct pitch -- rising
// means started or succeeded, falling means failed, a flat blip is chat.
package earcons

import (
	"fmt"
	"math"
	"sort"
	"sync"

	"github.com/exzork/mikkilens/packages/audio/devices"
)

// SampleRate is what every earcon is rendered at.
const SampleRate = 48000

const gapSeconds = 0.03

type step struct {
	frequency float64
	duration  float64
}

// patterns maps a name to its sequence of tones.
var patterns = map[string][]step{
	"listening": {{880.0, 0.09}},                                 // single, bright
	"ok":        {{660.0, 0.07}, {990.0, 0.10}},                  // rising pair
	"error":     {{420.0, 0.14}, {280.0, 0.22}},                  // falling, low, long
	"confirm":   {{520.0, 0.08}, {780.0, 0.08}, {520.0, 0.08}},   // up-down question
	"chat":      {{1180.0, 0.045}},                               // soft blip
	"superchat": {{880.0, 0.07}, {1100.0, 0.07}, {1320.0, 0.11}}, // rising triple
	"thinking":  {{600.0, 0.06}, {600.0, 0.06}},                  // two flat, "working"
}

// relativeVolume keeps chat well below the others: it fires constantly, and an
// alert that plays every few seconds stops being an alert.
var relativeVolume = map[string]float64{
	"chat":      0.45,
	"error":     1.0,
	"superchat": 0.9,
}

type cacheKey struct {
	name  string
	level int // millivolume, so the key stays comparable
}

var (
	cacheMu sync.Mutex
	cache   = map[cacheKey][]float32{}
)

// Names lists every earcon, in a stable order.
func Names() []string {
	names := make([]string, 0, len(patterns))
	for name := range patterns {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Render builds, and then remembers, the waveform for one earcon.
func Render(name string, volume float64) ([]float32, error) {
	pattern, ok := patterns[name]
	if !ok {
		return nil, fmt.Errorf("unknown earcon %q; known: %v", name, Names())
	}

	scale := 0.75
	if relative, ok := relativeVolume[name]; ok {
		scale = relative
	}
	level := volume * scale
	key := cacheKey{name: name, level: int(math.Round(level * 10000))}

	cacheMu.Lock()
	if cached, ok := cache[key]; ok {
		cacheMu.Unlock()
		return cached, nil
	}
	cacheMu.Unlock()

	gap := make([]float32, int(SampleRate*gapSeconds))
	wave := []float32{}
	for index, tone := range pattern {
		if index > 0 {
			wave = append(wave, gap...)
		}
		wave = append(wave, devices.Tone(tone.frequency, tone.duration, SampleRate, level)...)
	}

	cacheMu.Lock()
	cache[key] = wave
	cacheMu.Unlock()
	return wave, nil
}

// Known reports whether a name is an earcon.
func Known(name string) bool { _, ok := patterns[name]; return ok }

// ClearCache drops the rendered waveforms. Only tests need this.
func ClearCache() {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	cache = map[cacheKey][]float32{}
}
