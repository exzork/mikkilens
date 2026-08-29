// Package capture owns the microphone.
//
// One shared input stream feeds every consumer. Opening the device twice --
// once for the wake word and once to record a command -- risks Windows handing
// out an exclusive handle and leaving one of them deaf, which would look like
// MikkiLens randomly ignoring her.
//
// The stream keeps a short rolling pre-roll, so a command is not clipped when
// she starts speaking a moment before the hotkey registers.
package capture

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/gen2brain/malgo"

	"github.com/exzork/mikkilens/packages/audio/devices"
)

const (
	// SampleRate is what the recognizer and the wake word both want. miniaudio
	// resamples from whatever the device natively runs at, so nothing here has
	// to care that WASAPI shared mode refuses any other rate.
	SampleRate = 16000

	// FrameMS is the frame size everything downstream is built around.
	FrameMS = 30

	// FrameSamples is one frame at 16 kHz.
	FrameSamples = SampleRate * FrameMS / 1000

	prerollMS     = 400
	prerollFrames = prerollMS / FrameMS
)

// Error is a microphone failure worth speaking aloud.
type Error struct{ Reason string }

func (e *Error) Error() string { return e.Reason }

// Listener receives every frame. It runs on the audio thread, so it must
// return promptly and must not block.
type Listener func(frame []float32)

// Stream is a single always-open input stream that fans frames out.
type Stream struct {
	mu        sync.RWMutex
	device    *devices.Device
	handle    *malgo.Device
	listeners map[int]Listener
	nextID    int

	preroll    [][]float32
	partial    []float32
	scratch    []float32
	framesSeen int
	lastError  string
}

// NewStream prepares a stream on one device. A nil device means the system
// default microphone.
func NewStream(device *devices.Device) *Stream {
	return &Stream{device: device, listeners: map[int]Listener{}}
}

// Start opens the microphone.
func (s *Stream) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.handle != nil {
		return nil
	}

	allocated, err := devices.Context()
	if err != nil {
		return &Error{Reason: fmt.Sprintf("could not open the microphone: %v", err)}
	}

	settings := malgo.DefaultDeviceConfig(malgo.Capture)
	settings.SampleRate = SampleRate
	settings.Capture.Format = malgo.FormatF32
	settings.Capture.Channels = 1
	settings.PeriodSizeInFrames = FrameSamples
	if s.device != nil {
		settings.Capture.DeviceID = devices.Pointer(s.device.ID())
	}

	handle, err := malgo.InitDevice(allocated.Context, settings, malgo.DeviceCallbacks{
		Data: func(_, input []byte, _ uint32) { s.onAudio(input) },
		Stop: func() { s.noteUnexpectedStop() },
	})
	if err != nil {
		return &Error{Reason: fmt.Sprintf("could not open the microphone: %v", err)}
	}
	if err := handle.Start(); err != nil {
		handle.Uninit()
		return &Error{Reason: fmt.Sprintf("could not start the microphone: %v", err)}
	}

	s.handle = handle
	name := "system default"
	if s.device != nil {
		name = s.device.Name
	}
	slog.Info("microphone started", "device", name, "sample_rate", SampleRate)
	return nil
}

// noteUnexpectedStop records a microphone that went away on its own -- a
// headset unplugged mid-stream, most often. The engine watches for this and
// says so, because a silently dead microphone is indistinguishable from
// MikkiLens ignoring her.
func (s *Stream) noteUnexpectedStop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.handle != nil {
		s.lastError = "the microphone stopped unexpectedly"
	}
}

// Stop closes the microphone.
func (s *Stream) Stop() {
	s.mu.Lock()
	handle := s.handle
	s.handle = nil
	s.mu.Unlock()

	if handle == nil {
		return
	}
	_ = handle.Stop()
	handle.Uninit()
}

// Running reports whether the microphone is open.
func (s *Stream) Running() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.handle != nil
}

// Device is the microphone in use.
func (s *Stream) Device() *devices.Device {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.device
}

// FramesSeen is how many frames have arrived, which is how the settings page
// tells "the microphone is quiet" from "the microphone is dead".
func (s *Stream) FramesSeen() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.framesSeen
}

// LastError is the most recent problem miniaudio reported.
func (s *Stream) LastError() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastError
}

// AddListener registers a consumer and returns the function that removes it.
func (s *Stream) AddListener(listener Listener) func() {
	s.mu.Lock()
	id := s.nextID
	s.nextID++
	s.listeners[id] = listener
	s.mu.Unlock()

	return func() {
		s.mu.Lock()
		delete(s.listeners, id)
		s.mu.Unlock()
	}
}

// Preroll is the last fraction of a second of audio, so an utterance that
// began just before the trigger is not clipped.
func (s *Stream) Preroll() []float32 {
	s.mu.RLock()
	defer s.mu.RUnlock()

	total := 0
	for _, frame := range s.preroll {
		total += len(frame)
	}
	joined := make([]float32, 0, total)
	for _, frame := range s.preroll {
		joined = append(joined, frame...)
	}
	return joined
}

// onAudio runs on the audio thread. It re-blocks into exact frames, because
// miniaudio does not promise the block size we asked for and everything
// downstream is built around a fixed frame.
func (s *Stream) onAudio(input []byte) {
	s.mu.Lock()
	s.scratch = devices.ReadFloat32(input, s.scratch)
	chunk := append(s.partial, s.scratch...)

	count := len(chunk) / FrameSamples
	frames := make([][]float32, 0, count)
	for index := 0; index < count; index++ {
		frame := make([]float32, FrameSamples)
		copy(frame, chunk[index*FrameSamples:(index+1)*FrameSamples])
		frames = append(frames, frame)

		s.preroll = append(s.preroll, frame)
		if len(s.preroll) > prerollFrames {
			s.preroll = s.preroll[len(s.preroll)-prerollFrames:]
		}
	}
	s.partial = append(s.partial[:0], chunk[count*FrameSamples:]...)
	s.framesSeen += count

	listeners := make([]Listener, 0, len(s.listeners))
	for _, listener := range s.listeners {
		listeners = append(listeners, listener)
	}
	s.mu.Unlock()

	for _, frame := range frames {
		for _, listener := range listeners {
			deliver(listener, frame)
		}
	}
}

// deliver keeps one bad listener from killing the audio thread, which would
// take the microphone down for everything.
func deliver(listener Listener, frame []float32) {
	defer func() {
		if problem := recover(); problem != nil {
			slog.Error("microphone listener panicked", "panic", problem)
		}
	}()
	listener(frame)
}

// -- recording ----------------------------------------------------------------

// Utterance is one recorded command.
type Utterance struct {
	Audio          []float32 // mono float32 at 16 kHz
	Duration       float64
	EndedOnSilence bool // false means it hit the length limit
}

// IsEmpty reports whether nothing was captured.
func (u Utterance) IsEmpty() bool { return len(u.Audio) == 0 }

// RecorderOptions shape one recording.
type RecorderOptions struct {
	Aggressiveness int
	SilenceMS      int
	MaxSeconds     float64
	StartTimeoutS  float64
	IncludePreroll bool
}

func (o RecorderOptions) withDefaults() RecorderOptions {
	if o.SilenceMS <= 0 {
		o.SilenceMS = 700
	}
	if o.MaxSeconds <= 0 {
		o.MaxSeconds = 12.0
	}
	if o.StartTimeoutS <= 0 {
		o.StartTimeoutS = 4.0
	}
	return o
}

// Record captures one spoken command, ending it on a pause.
//
// A closed release channel ends the recording early: that is how hold-to-talk
// works, where letting go of the key finishes the utterance without waiting.
func Record(stream *Stream, options RecorderOptions, release <-chan struct{}) Utterance {
	options = options.withDefaults()
	detector := NewVAD(options.Aggressiveness)

	incoming := make(chan []float32, 256)
	remove := stream.AddListener(func(frame []float32) {
		select {
		case incoming <- frame:
		default: // the consumer fell behind; dropping is better than blocking audio
		}
	})
	defer remove()

	collected := []float32{}
	if options.IncludePreroll {
		collected = append(collected, stream.Preroll()...)
	}

	silenceLimit := max(1, options.SilenceMS/FrameMS)
	started := time.Now()
	speechSeen := false
	trailingSilence := 0
	endedOnSilence := false
	released := false

	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()

loop:
	for {
		select {
		case <-release:
			released = true
			break loop

		case frame := <-incoming:
			collected = append(collected, frame...)
			if detector.IsSpeech(frame) {
				speechSeen = true
				trailingSilence = 0
			} else if speechSeen {
				trailingSilence++
			}
			if speechSeen && trailingSilence >= silenceLimit {
				endedOnSilence = true
				break loop
			}

		case <-ticker.C:
		}

		elapsed := time.Since(started).Seconds()
		if !speechSeen && elapsed > options.StartTimeoutS {
			break // she never started speaking
		}
		if elapsed > options.MaxSeconds {
			break
		}
	}

	// On hold-to-talk, trust the key rather than the detector: she may have
	// spoken too quietly for it, and discarding the audio would look exactly
	// like being ignored. Let the recognizer decide whether there were words.
	if !speechSeen && !released {
		return Utterance{Audio: nil, EndedOnSilence: endedOnSilence}
	}
	return Utterance{
		Audio:          collected,
		Duration:       float64(len(collected)) / float64(SampleRate),
		EndedOnSilence: endedOnSilence,
	}
}

// Measure records briefly and returns the loudest level it heard. The setup
// wizard uses it to say "the microphone sounds good" or "I heard nothing".
func Measure(stream *Stream, duration time.Duration) float64 {
	var (
		mu   sync.Mutex
		peak float64
	)
	remove := stream.AddListener(func(frame []float32) {
		level := Level(frame)
		mu.Lock()
		if level > peak {
			peak = level
		}
		mu.Unlock()
	})
	defer remove()

	time.Sleep(duration)
	mu.Lock()
	defer mu.Unlock()
	return peak
}
