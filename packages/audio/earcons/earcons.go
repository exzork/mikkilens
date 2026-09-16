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

// minimumSeconds is how long a sound has to be before a Bluetooth headset
// renders it at all.
//
// Measured on an M180BT, playing tones of rising length at the wake tone's own
// pitch and volume: 90 ms and 150 ms were not heard, 250 ms was, and three
// second tones came through in full. It is not the link being asleep -- the
// 150 ms tone was missed a second and a half after a tone that had just been
// heard, and the lead-in silence does nothing to help, because what wakes the
// link is audio rather than silence. The codec simply needs longer than a
// blip before anything comes out of the headphones.
//
// So the listening tone, at 90 ms, was never heard on those headphones: she
// said the wake word, nothing acknowledged it, and the only way to know it had
// worked was to keep talking and find out. Every earcon is now at least this
// long, with a margin over the 250 ms that was audible.
const minimumSeconds = 0.30

// patterns maps a name to its sequence of tones.
//
// The contours are what tell them apart by ear -- rising means started or
// succeeded, falling means failed, a flat blip is chat -- and the lengths are
// set so the whole sound clears minimumSeconds, gaps included.
var patterns = map[string][]step{
	"listening": {{880.0, 0.30}},                                  // single, bright
	"ok":        {{660.0, 0.14}, {990.0, 0.20}},                   // rising pair
	"error":     {{420.0, 0.16}, {280.0, 0.26}},                   // falling, low, long
	"confirm":   {{520.0, 0.11}, {780.0, 0.11}, {520.0, 0.13}},    // up-down question
	"chat":      {{1180.0, 0.30}},                                 // soft blip
	"superchat": {{880.0, 0.12}, {1100.0, 0.12}, {1320.0, 0.20}},  // rising triple
	"donation":  {{1320.0, 0.12}, {1660.0, 0.12}, {1980.0, 0.22}}, // brighter, higher
	"thinking":  {{600.0, 0.14}, {600.0, 0.14}},                   // two flat, "working"
}

// relativeVolume keeps chat well below the others: it fires constantly, and an
// alert that plays every few seconds stops being an alert.
var relativeVolume = map[string]float64{
	"chat":      0.45,
	"error":     1.0,
	"superchat": 0.9,
	"donation":  0.9,
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
