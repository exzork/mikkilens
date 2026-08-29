package tts

import (
	"context"
	"sync"

	"github.com/exzork/mikkilens/packages/audio/devices"
)

// Speaker owns the output device. It plays one thing at a time, and it can be
// cut off mid-word, which is what lets an error preempt a chat message.
type Speaker struct {
	mu        sync.Mutex   // serializes playback: one voice at a time
	deviceMu  sync.RWMutex // guards the chosen device, which settings can change
	device    *devices.Device
	interrupt *devices.Interrupt
}

// NewSpeaker builds a speaker for one output device. A nil device means
// whatever Windows has set as the default.
func NewSpeaker(device *devices.Device) *Speaker {
	return &Speaker{device: device, interrupt: devices.NewInterrupt()}
}

// SetDevice changes where the voice comes out, taking effect on the next
// utterance rather than cutting off the current one.
func (s *Speaker) SetDevice(device *devices.Device) {
	s.deviceMu.Lock()
	s.device = device
	s.deviceMu.Unlock()
}

// Device is the output device currently in use.
func (s *Speaker) Device() *devices.Device {
	s.deviceMu.RLock()
	defer s.deviceMu.RUnlock()
	return s.device
}

// Stop cuts playback short. It is safe to call from any goroutine.
func (s *Speaker) Stop() { s.interrupt.Stop() }

// Play blocks until the audio has been heard. It returns false when something
// interrupted it, which is the caller's cue to put the utterance back on the
// queue rather than lose it.
func (s *Speaker) Play(audio Audio) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.interrupt.Reset()
	completed, err := devices.Play(
		s.Device(), audio.Samples, audio.SampleRate, audio.Channels, s.interrupt)
	if err != nil {
		return false, &Error{Reason: err.Error()}
	}
	return completed, nil
}

// Say synthesizes and plays in one step. The setup wizard and the command line
// use it; the speech bus does the two halves separately so it can reorder the
// queue between them.
func (s *Speaker) Say(ctx context.Context, text string, options Options) (bool, error) {
	audio, err := Synthesize(ctx, text, options)
	if err != nil {
		return false, err
	}
	return s.Play(audio)
}
