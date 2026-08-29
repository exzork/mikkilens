// Package stt turns recorded audio into text.
//
// The Python version bound faster-whisper directly, which meant a Python
// runtime, a CUDA toolchain and a pip install on the streaming machine. Here
// recognition is an interface with two implementations behind it, so the same
// engine runs against a local whisper.cpp build or a remote endpoint without
// anything above this package knowing which.
//
// The decode language is pinned rather than auto-detected. A command is only a
// few words, which is not much for language detection to work with, and a wrong
// guess turns a working command into a mishearing.
package stt

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/exzork/mikkilens/packages/core/config"
)

// SampleRate is what every backend expects.
const SampleRate = 16000

// Transcript is one recognized utterance.
type Transcript struct {
	Text       string  `json:"text"`
	Language   string  `json:"language"`
	Duration   float64 `json:"duration_s"`
	Elapsed    float64 `json:"elapsed_s"`
	Confidence float64 `json:"confidence"`
}

// IsEmpty reports whether nothing was recognized.
func (t Transcript) IsEmpty() bool { return strings.TrimSpace(t.Text) == "" }

// Error is a recognition failure worth reporting aloud.
type Error struct{ Reason string }

func (e *Error) Error() string { return e.Reason }

// Backend is one way of turning audio into text.
type Backend interface {
	// Name is what the settings page shows, e.g. "whisper.cpp small on cuda".
	Name() string

	// Load prepares the backend. It may be slow the first time, which is why
	// the engine runs it in the background rather than blocking startup.
	Load(ctx context.Context) error

	// Transcribe recognizes mono float32 audio at 16 kHz.
	Transcribe(ctx context.Context, audio []float32, language string) (Transcript, error)

	// Close releases whatever Load acquired.
	Close() error
}

// Transcriber wraps a backend, loading it lazily so startup is not blocked on
// a model download.
type Transcriber struct {
	mu       sync.RWMutex
	settings config.STT
	language string
	backend  Backend
	loaded   bool
}

// New builds a transcriber from configuration.
func New(settings config.STT, language string) *Transcriber {
	return &Transcriber{settings: settings, language: language}
}

// SetLanguage changes the decode language without reloading the model.
func (t *Transcriber) SetLanguage(language string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.language = language
}

// SetConfig swaps the settings. The caller unloads and reloads if the model
// itself changed.
func (t *Transcriber) SetConfig(settings config.STT) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.settings = settings
}

// Loaded reports whether recognition is ready.
func (t *Transcriber) Loaded() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.loaded
}

// Describe names the backend in use, for the settings page and the self test.
func (t *Transcriber) Describe() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.backend == nil {
		return "not loaded"
	}
	return t.backend.Name()
}

// Load prepares recognition. It is safe to call more than once.
func (t *Transcriber) Load(ctx context.Context) error {
	t.mu.Lock()
	if t.loaded {
		t.mu.Unlock()
		return nil
	}
	settings := t.settings
	t.mu.Unlock()

	backend, err := chooseBackend(settings)
	if err != nil {
		return err
	}
	if err := backend.Load(ctx); err != nil {
		return err
	}

	t.mu.Lock()
	t.backend = backend
	t.loaded = true
	t.mu.Unlock()
	return nil
}

// Unload releases the model, which is what a settings change that swaps models
// does before loading the new one.
func (t *Transcriber) Unload() {
	t.mu.Lock()
	backend := t.backend
	t.backend = nil
	t.loaded = false
	t.mu.Unlock()

	if backend != nil {
		_ = backend.Close()
	}
}

// Transcribe recognizes one utterance, loading the backend first if needed.
func (t *Transcriber) Transcribe(ctx context.Context, audio []float32) (Transcript, error) {
	t.mu.RLock()
	backend, loaded, language := t.backend, t.loaded, t.language
	t.mu.RUnlock()

	if !loaded || backend == nil {
		if err := t.Load(ctx); err != nil {
			return Transcript{}, err
		}
		t.mu.RLock()
		backend, language = t.backend, t.language
		t.mu.RUnlock()
	}

	started := time.Now()
	transcript, err := backend.Transcribe(ctx, audio, language)
	if err != nil {
		return Transcript{}, err
	}
	transcript.Duration = float64(len(audio)) / float64(SampleRate)
	transcript.Elapsed = time.Since(started).Seconds()
	if transcript.Language == "" {
		transcript.Language = language
	}
	return transcript, nil
}

// chooseBackend picks a backend from configuration.
//
// "auto" prefers a local whisper.cpp build, because local recognition is free,
// private, works when her connection drops, and on a GPU is faster than any
// network round trip. It falls back to a configured endpoint only when there
// is no local model to use.
func chooseBackend(settings config.STT) (Backend, error) {
	switch strings.ToLower(strings.TrimSpace(settings.Backend)) {
	case "whispercpp", "local":
		return newWhisperCPP(settings)
	case "openai", "remote", "http":
		return newRemote(settings)
	case "", "auto":
		if local, err := newWhisperCPP(settings); err == nil {
			return local, nil
		}
		if settings.BaseURL != "" {
			return newRemote(settings)
		}
		return nil, &Error{Reason: "no speech recognition is set up: install a " +
			"whisper.cpp build, or set a transcription endpoint in the settings"}
	default:
		return nil, &Error{Reason: "unknown speech recognition backend " + settings.Backend}
	}
}

// cleanTranscript strips the bracketed markers Whisper emits for non-speech,
// which would otherwise be read aloud or matched as a command.
func cleanTranscript(text string) string {
	text = strings.TrimSpace(text)
	for {
		open := strings.IndexAny(text, "[(")
		if open < 0 {
			break
		}
		closer := byte(']')
		if text[open] == '(' {
			closer = ')'
		}
		end := strings.IndexByte(text[open:], closer)
		if end < 0 {
			break
		}
		text = strings.TrimSpace(text[:open] + text[open+end+1:])
	}
	return strings.TrimSpace(strings.Join(strings.Fields(text), " "))
}
