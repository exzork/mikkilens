// Package wake listens for a trigger word, locally.
//
// Local and free, with no API key and no audio leaving the machine. The
// microphone is always open for this, so anything cloud-based would mean
// streaming her whole broadcast to a third party.
//
// The detector runs openWakeWord's three-stage ONNX pipeline: a mel
// spectrogram, an embedding model, and one small classifier per wake word. The
// models are the ones openWakeWord publishes; MikkiLens only runs them.
//
// The threshold is deliberately exposed in config. A VTuber talks
// continuously, and a false trigger mid-stream is worse than an occasional
// missed one -- so the default leans towards missing.
package wake

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/exzork/mikkilens/packages/core/paths"
)

// ChunkSamples is openWakeWord's window: 80 ms of 16 kHz audio.
const ChunkSamples = 1280

// Error is a wake-word failure. It is always reported aloud rather than
// leaving her wondering why the trigger word stopped working.
type Error struct{ Reason string }

func (e *Error) Error() string { return e.Reason }

// Detected is called when the trigger word fires.
type Detected func(name string, score float64)

// Options configure a detector.
type Options struct {
	Model      string
	Threshold  float64
	CooldownS  float64
	OnDetected Detected
}

// Detector feeds microphone frames through the pipeline and fires on a match.
type Detector struct {
	mu        sync.Mutex
	options   Options
	pipeline  *pipeline
	buffer    []float32
	lastFired time.Time
	enabled   bool
	lastScore float64

	// Kept apart from enabled on purpose. Two different things switch the
	// detector off -- a command being recorded, and MikkiLens talking -- and
	// they overlap: she is spoken to, and answers before the sentence has
	// finished. One flag would let whichever finished first switch listening
	// back on underneath the other.
	speaking bool
	// quietUntil holds it off a moment longer than the speech itself. The
	// pipeline scores the last 1.28 seconds of audio, so the tail of her own
	// name is still in there when the speaker goes quiet.
	quietUntil time.Time

	// Scoring happens on this channel's goroutine rather than on the audio
	// thread. Six milliseconds of inference inside the capture callback is six
	// milliseconds the microphone is not being drained, and a late drain is a
	// gap in what she said.
	chunks  chan []float32
	stop    chan struct{}
	worker  sync.WaitGroup
	pending atomic.Int64
}

// New prepares a detector. Nothing is loaded until Load is called.
func New(options Options) *Detector {
	if options.Threshold <= 0 {
		options.Threshold = 0.6
	}
	if options.CooldownS <= 0 {
		options.CooldownS = 2.0
	}
	return &Detector{options: options, enabled: true}
}

// ModelName is the wake word in use.
func (d *Detector) ModelName() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.options.Model
}

// LastScore is the most recent confidence, which the settings page shows so
// she can tune the threshold by watching it while she talks.
func (d *Detector) LastScore() float64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.lastScore
}

// Loaded reports whether the models are ready.
func (d *Detector) Loaded() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.pipeline != nil
}

// Load opens the ONNX runtime and the three models.
func (d *Detector) Load() error {
	d.mu.Lock()
	if d.pipeline != nil {
		d.mu.Unlock()
		return nil
	}
	model := d.options.Model
	d.mu.Unlock()

	installBuiltin()

	built, err := newPipeline(model)
	if err != nil {
		return err
	}

	d.mu.Lock()
	d.pipeline = built
	d.chunks = make(chan []float32, 4)
	d.stop = make(chan struct{})
	chunks, stop := d.chunks, d.stop
	d.mu.Unlock()

	d.worker.Add(1)
	go d.score(chunks, stop)

	slog.Info("wake word ready", "model", model)
	return nil
}

// score is the goroutine that runs the models, one chunk at a time.
func (d *Detector) score(chunks <-chan []float32, stop <-chan struct{}) {
	defer d.worker.Done()
	for {
		select {
		case <-stop:
			return
		case chunk := <-chunks:
			d.predict(chunk)
			d.pending.Add(-1)
		}
	}
}

// Close releases the models.
func (d *Detector) Close() {
	d.mu.Lock()
	built, stop := d.pipeline, d.stop
	d.pipeline, d.stop, d.chunks = nil, nil, nil
	d.mu.Unlock()

	if stop != nil {
		close(stop)
		d.worker.Wait()
	}
	if built != nil {
		built.close()
	}
}

// speechTail is how long the detector stays off after the speakers go quiet.
//
// The scoring window is 1.28 seconds wide, so the moment playback ends the last
// second of it is still MikkiLens talking. Long enough to cover that and the
// room's own echo of it; short enough that she can answer straight back.
const speechTail = 1500 * time.Millisecond

// Pause stops listening, which is what happens while a command is being
// recorded: the trigger word must not fire on the command itself.
func (d *Detector) Pause() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.enabled = false
}

// Resume starts listening again, from a clean slate.
func (d *Detector) Resume() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.buffer = d.buffer[:0]
	if d.pipeline != nil {
		d.pipeline.reset()
	}
	d.lastFired = time.Now()
	d.enabled = true
}

// Enabled reports whether the detector is listening.
func (d *Detector) Enabled() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.enabled
}

// SetSpeaking switches the detector off while MikkiLens is talking, and back
// on a moment after it stops.
//
// Her name is the wake word, so anything that says it -- reading a command
// back, answering a question, saying "MikkiLens is listening" -- comes out of
// the speakers and straight back into the microphone, and the detector cannot
// tell that voice from hers. Nothing is gained by scoring MikkiLens against
// its own name.
//
// The buffer and the pipeline are cleared on the way out rather than the way
// in, because what has to be thrown away is the audio recorded during the
// speech, not before it.
func (d *Detector) SetSpeaking(speaking bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if speaking {
		d.speaking = true
		return
	}
	d.speaking = false
	d.quietUntil = time.Now().Add(speechTail)
	d.buffer = d.buffer[:0]
	if d.pipeline != nil {
		d.pipeline.reset()
	}
}

// listening reports whether audio should be scored at all. Callers hold the
// lock.
func (d *Detector) listening() bool {
	return d.enabled && !d.speaking && time.Now().After(d.quietUntil)
}

// Feed accepts one frame of 16 kHz mono audio from the microphone stream.
func (d *Detector) Feed(frame []float32) {
	d.mu.Lock()
	if !d.listening() || d.pipeline == nil {
		d.mu.Unlock()
		return
	}
	d.buffer = append(d.buffer, frame...)

	ready := [][]float32{}
	for len(d.buffer) >= ChunkSamples {
		chunk := make([]float32, ChunkSamples)
		copy(chunk, d.buffer[:ChunkSamples])
		ready = append(ready, chunk)
		d.buffer = d.buffer[ChunkSamples:]
	}
	chunks := d.chunks
	d.mu.Unlock()

	for _, chunk := range ready {
		select {
		case chunks <- chunk:
			d.pending.Add(1)
		default:
			// Scoring has fallen behind. Dropping a chunk costs one chance to
			// hear the wake word; blocking here would stall the microphone for
			// everything, including the command she is speaking right now.
			slog.Debug("wake word scoring fell behind; dropped a chunk")
		}
	}
}

func (d *Detector) predict(chunk []float32) {
	d.mu.Lock()
	built := d.pipeline
	threshold, cooldown := d.options.Threshold, d.options.CooldownS
	d.mu.Unlock()

	if built == nil {
		return
	}
	score, err := built.score(chunk)
	if err != nil {
		// A bad frame must not kill the microphone thread, and it must not
		// spam the log either: this runs twelve times a second.
		slog.Debug("wake word prediction failed", "error", err)
		return
	}

	d.mu.Lock()
	d.lastScore = score
	fire := score >= threshold &&
		time.Since(d.lastFired).Seconds() >= cooldown
	if fire {
		d.lastFired = time.Now()
	}
	callback, name := d.options.OnDetected, d.options.Model
	d.mu.Unlock()

	if fire && callback != nil {
		safely(callback, name, score)
	}
}

func safely(callback Detected, name string, score float64) {
	defer func() {
		if problem := recover(); problem != nil {
			slog.Error("wake word callback panicked", "panic", problem)
		}
	}()
	callback(name, score)
}

// modelDirectories are where the openWakeWord ONNX files are looked for.
func modelDirectories(root string) []string {
	return []string{
		filepath.Join(root, "wakeword"),
		filepath.Join(root, "openwakeword"),
		root,
	}
}

// findModelFile locates one ONNX file by name, tolerating the several layouts
// openWakeWord has shipped over the years.
func findModelFile(root, name string) (string, error) {
	wanted := []string{name + ".onnx", name + "_v0.1.onnx", name}
	for _, directory := range modelDirectories(root) {
		for _, candidate := range wanted {
			path := filepath.Join(directory, candidate)
			if info, err := os.Stat(path); err == nil && !info.IsDir() {
				return path, nil
			}
		}
		// Fall back to a prefix match, which catches the versioned names.
		entries, err := os.ReadDir(directory)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			file := entry.Name()
			if strings.HasPrefix(file, name) && strings.HasSuffix(file, ".onnx") {
				return filepath.Join(directory, file), nil
			}
		}
	}
	return "", fmt.Errorf("%s.onnx was not found under %s", name, root)
}

// -- what is installed --------------------------------------------------------

// versionSuffix is openWakeWord's habit of stamping the training run into the
// file name: hey_jarvis_v0.1.onnx is the "hey_jarvis" wake word.
var versionSuffix = regexp.MustCompile(`_v\d+(\.\d+)*$`)

// shared are the two stages every wake word runs through. They sit in the same
// directory as the wake words themselves and are not wake words.
var shared = map[string]bool{
	"melspectrogram":  true,
	"embedding_model": true,
	"silero_vad":      true,
}

// Installed lists the wake words whose model is actually on the machine.
//
// The settings page offers this list rather than a text box. A name that is
// not installed cannot be typed into working: the detector fails to load, the
// engine falls back to the hotkey, and what she sees is a microphone that
// never answers -- which is indistinguishable from one that is not listening
// at all.
func Installed() []string {
	// MikkiLens's own wake word ships inside the executable, so this is where
	// it lands on disk: the list is built by scanning the directory, and a
	// model that is not in the directory cannot be offered.
	installBuiltin()

	seen := map[string]bool{}
	names := []string{}

	for _, directory := range modelDirectories(paths.ModelsDir()) {
		entries, err := os.ReadDir(directory)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".onnx") {
				continue
			}
			name := WakeWordName(entry.Name())
			if name == "" || shared[name] || seen[name] {
				continue
			}
			seen[name] = true
			names = append(names, name)
		}
	}

	sort.Strings(names)
	return names
}

// WakeWordName turns a model file name into the name the config file uses.
func WakeWordName(file string) string {
	name := strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))
	return versionSuffix.ReplaceAllString(name, "")
}

// RuntimeReady reports whether the ONNX runtime is where the wake word needs
// it, without loading anything.
//
// The settings page asks before it offers the list, so an empty list is
// explained as "the runtime is missing" rather than shown as a dropdown with
// nothing in it.
func RuntimeReady() error {
	_, err := findRuntimeLibrary()
	return err
}
