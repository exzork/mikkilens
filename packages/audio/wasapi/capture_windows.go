//go:build windows

package wasapi

import (
	"runtime"
	"sync"
	"time"
	"unsafe"
)

// Recorder is an open microphone stream.
//
// One stream feeds every consumer. Opening the device twice -- once for the
// wake word and once to record a command -- risks Windows handing out an
// exclusive handle and leaving one of them deaf, which looks exactly like
// MikkiLens randomly ignoring her.
type Recorder struct {
	mu      sync.Mutex
	running bool
	stop    chan struct{}
	done    chan struct{}

	sampleRate int
	channels   int
	lastError  error
}

// OnAudio receives interleaved float32 frames as they arrive. It runs on the
// capture thread, so it must return promptly.
type OnAudio func(samples []float32)

// StartCapture opens a microphone and streams it to the callback.
//
// An empty device id means the system default. The sample rate and channel
// count are what the caller wants; WASAPI converts from whatever the hardware
// runs at.
func StartCapture(deviceID string, sampleRate, channels int, onAudio OnAudio) (*Recorder, error) {
	if channels < 1 {
		channels = 1
	}

	recorder := &Recorder{
		sampleRate: sampleRate,
		channels:   channels,
		stop:       make(chan struct{}),
		done:       make(chan struct{}),
	}

	ready := make(chan error, 1)
	go recorder.run(deviceID, onAudio, ready)

	if err := <-ready; err != nil {
		return nil, err
	}
	recorder.mu.Lock()
	recorder.running = true
	recorder.mu.Unlock()
	return recorder, nil
}

// Stop closes the microphone.
func (r *Recorder) Stop() {
	r.mu.Lock()
	if !r.running {
		r.mu.Unlock()
		return
	}
	r.running = false
	r.mu.Unlock()

	close(r.stop)
	select {
	case <-r.done:
	case <-time.After(2 * time.Second):
	}
}

// Running reports whether the microphone is open.
func (r *Recorder) Running() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.running
}

// LastError is the most recent problem the capture thread hit. A microphone
// that dies quietly is indistinguishable from one being ignored, so this is
// what the engine watches to say so.
func (r *Recorder) LastError() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastError
}

func (r *Recorder) note(err error) {
	r.mu.Lock()
	r.lastError = err
	r.mu.Unlock()
}

func (r *Recorder) run(deviceID string, onAudio OnAudio, ready chan<- error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer close(r.done)

	scope, err := enterCOM()
	if err != nil {
		ready <- err
		return
	}
	defer scope.leave()

	enumerator, err := createEnumerator()
	if err != nil {
		ready <- err
		return
	}
	defer release(enumerator)

	device, err := openDevice(enumerator, deviceID, Capture)
	if err != nil {
		ready <- err
		return
	}

	client, err := activate(device, Capture, r.sampleRate, r.channels)
	if err != nil {
		release(device)
		ready <- err
		return
	}
	defer client.close()

	if !client.isFloat32() {
		ready <- &Error{Op: "the microphone offers no float32 format", HResult: 0}
		return
	}

	capture, err := client.service(&iidIAudioCaptureClient)
	if err != nil {
		ready <- err
		return
	}
	defer release(capture)

	if err := client.start(); err != nil {
		ready <- err
		return
	}
	defer client.stop()

	deviceChannels := client.channels()
	wantedChannels := r.channels

	// Poll at a fraction of the buffer length: often enough that frames arrive
	// smoothly, rarely enough that it costs nothing while she is streaming.
	interval := time.Duration(client.frames) * time.Second /
		time.Duration(client.sampleRate()) / 4
	if interval < 5*time.Millisecond {
		interval = 5 * time.Millisecond
	}

	ready <- nil

	for {
		select {
		case <-r.stop:
			return
		default:
		}

		var packet uint32
		hr := call(capture, 5, uintptr(unsafe.Pointer(&packet))) // GetNextPacketSize
		if int32(hr) < 0 {
			r.note(check("IAudioCaptureClient::GetNextPacketSize", hr))
			return
		}
		if packet == 0 {
			time.Sleep(interval)
			continue
		}

		var (
			buffer    unsafe.Pointer
			frames    uint32
			flags     uint32
			position  uint64
			timestamp uint64
		)
		hr = call(capture, 3, // GetBuffer
			uintptr(unsafe.Pointer(&buffer)),
			uintptr(unsafe.Pointer(&frames)),
			uintptr(unsafe.Pointer(&flags)),
			uintptr(unsafe.Pointer(&position)),
			uintptr(unsafe.Pointer(&timestamp)))
		if int32(hr) < 0 {
			r.note(check("IAudioCaptureClient::GetBuffer", hr))
			return
		}

		if frames > 0 && onAudio != nil {
			// A copy, because the buffer belongs to WASAPI and is reused as
			// soon as it is released.
			samples := make([]float32, int(frames)*wantedChannels)

			const flagSilent = 0x2
			if flags&flagSilent == 0 && buffer != nil {
				raw := unsafe.Slice((*float32)(buffer), int(frames)*deviceChannels)
				if deviceChannels == wantedChannels {
					copy(samples, raw)
				} else {
					downmix(raw, deviceChannels, samples, wantedChannels, int(frames))
				}
			}
			deliver(onAudio, samples)
		}

		if hr := call(capture, 4, uintptr(frames)); int32(hr) < 0 { // ReleaseBuffer
			r.note(check("IAudioCaptureClient::ReleaseBuffer", hr))
			return
		}
	}
}

// downmix averages the device's channels into ours, which is what turns a
// stereo headset microphone into the mono the recognizer expects.
func downmix(source []float32, sourceChannels int, target []float32, targetChannels, frames int) {
	for frame := 0; frame < frames; frame++ {
		total := float32(0)
		for channel := 0; channel < sourceChannels; channel++ {
			total += source[frame*sourceChannels+channel]
		}
		average := total / float32(sourceChannels)

		for channel := 0; channel < targetChannels; channel++ {
			target[frame*targetChannels+channel] = average
		}
	}
}

// deliver keeps one bad consumer from taking the capture thread down, which
// would take the microphone with it.
func deliver(onAudio OnAudio, samples []float32) {
	defer func() { _ = recover() }()
	onAudio(samples)
}
