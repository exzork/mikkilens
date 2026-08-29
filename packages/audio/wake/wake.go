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
	"strings"
	"sync"
	"time"
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

	built, err := newPipeline(model)
	if err != nil {
		return err
	}

	d.mu.Lock()
	d.pipeline = built
	d.mu.Unlock()
	slog.Info("wake word ready", "model", model)
	return nil
}

// Close releases the models.
func (d *Detector) Close() {
	d.mu.Lock()
	built := d.pipeline
	d.pipeline = nil
	d.mu.Unlock()
	if built != nil {
		built.close()
	}
}

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

// Feed accepts one frame of 16 kHz mono audio from the microphone stream.
func (d *Detector) Feed(frame []float32) {
	d.mu.Lock()
	if !d.enabled || d.pipeline == nil {
		d.mu.Unlock()
		return
	}
	d.buffer = append(d.buffer, frame...)

	chunks := [][]float32{}
	for len(d.buffer) >= ChunkSamples {
		chunk := make([]float32, ChunkSamples)
		copy(chunk, d.buffer[:ChunkSamples])
		chunks = append(chunks, chunk)
		d.buffer = d.buffer[ChunkSamples:]
	}
	d.mu.Unlock()

	for _, chunk := range chunks {
		d.predict(chunk)
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
