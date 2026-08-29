package devices

import (
	"encoding/binary"
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/gen2brain/malgo"

	"github.com/exzork/mikkilens/packages/audio"
)

// Playback opens a device per sound rather than holding one open for the life
// of the process. miniaudio's shared-mode open costs a few milliseconds, which
// is nothing beside the second a synthesized sentence takes, and it means a
// device that disappears mid-stream (a headset unplugged, a monitor turned
// off) costs one failed sound rather than every sound afterwards.

// Interrupt lets a caller cut a sound short from another goroutine.
type Interrupt struct{ stopped atomic.Bool }

// NewInterrupt makes an interrupt that has not fired.
func NewInterrupt() *Interrupt { return &Interrupt{} }

// Stop cuts the sound short. It is safe to call from any goroutine.
func (i *Interrupt) Stop() { i.stopped.Store(true) }

// Reset arms the interrupt again for the next sound.
func (i *Interrupt) Reset() { i.stopped.Store(false) }

// Stopped reports whether the interrupt has fired.
func (i *Interrupt) Stopped() bool { return i.stopped.Load() }

// Play writes interleaved float32 samples to one device and blocks until they
// have been heard. It returns false when the interrupt cut it short.
//
// A nil device means whatever Windows has set as the default.
func Play(device *Device, samples []float32, sampleRate, channels int, interrupt *Interrupt) (bool, error) {
	if len(samples) == 0 {
		return true, nil
	}
	if channels < 1 {
		channels = 1
	}

	if audio.Silent() {
		// Stay interruptible and keep roughly real-time pacing, so the speech
		// queue behaves exactly as it would with sound.
		return waitSilently(len(samples)/channels, sampleRate, interrupt), nil
	}

	allocated, err := Context()
	if err != nil {
		return false, err
	}

	settings := malgo.DefaultDeviceConfig(malgo.Playback)
	settings.SampleRate = uint32(sampleRate)
	settings.Playback.Format = malgo.FormatF32
	settings.Playback.Channels = uint32(channels)
	if device != nil {
		settings.Playback.DeviceID = devicePointer(device.ID())
	}

	var (
		mu       sync.Mutex
		position int
		finished = make(chan struct{})
		once     sync.Once
	)
	done := func() { once.Do(func() { close(finished) }) }

	settings.DataCallback = nil
	callbacks := malgo.DeviceCallbacks{
		Data: func(output, _ []byte, frameCount uint32) {
			wanted := int(frameCount) * channels

			mu.Lock()
			remaining := len(samples) - position
			taking := wanted
			if taking > remaining {
				taking = remaining
			}
			chunk := samples[position : position+taking]
			position += taking
			exhausted := position >= len(samples)
			mu.Unlock()

			writeFloat32(output, chunk)
			// Anything we could not fill has to be silence, or miniaudio plays
			// whatever was left in the buffer, which is a burst of noise.
			for index := taking * 4; index < wanted*4 && index < len(output); index++ {
				output[index] = 0
			}
			if exhausted {
				done()
			}
		},
	}

	handle, err := malgo.InitDevice(allocated.Context, settings, callbacks)
	if err != nil {
		return false, fmt.Errorf("could not open the output device: %w", err)
	}
	defer handle.Uninit()

	if err := handle.Start(); err != nil {
		return false, fmt.Errorf("could not start the output device: %w", err)
	}
	defer func() { _ = handle.Stop() }()

	// The callback signals when the last sample has been handed over; give the
	// device one buffer's worth of grace so the tail is actually heard.
	tail := time.Duration(float64(time.Second) * 0.05)
	timeout := time.Duration(float64(len(samples)/channels)/float64(sampleRate)*float64(time.Second)) +
		2*time.Second

	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.After(timeout)

	for {
		select {
		case <-finished:
			time.Sleep(tail)
			return true, nil
		case <-deadline:
			return true, nil
		case <-ticker.C:
			if interrupt != nil && interrupt.Stopped() {
				return false, nil
			}
		}
	}
}

func waitSilently(frames, sampleRate int, interrupt *Interrupt) bool {
	duration := time.Duration(float64(frames) / float64(sampleRate) * float64(time.Second))
	deadline := time.Now().Add(duration)
	for time.Now().Before(deadline) {
		if interrupt != nil && interrupt.Stopped() {
			return false
		}
		time.Sleep(5 * time.Millisecond)
	}
	return true
}

// writeFloat32 packs samples into miniaudio's byte buffer.
func writeFloat32(destination []byte, samples []float32) {
	for index, sample := range samples {
		offset := index * 4
		if offset+4 > len(destination) {
			return
		}
		binary.LittleEndian.PutUint32(destination[offset:], math.Float32bits(sample))
	}
}

// ReadFloat32 unpacks miniaudio's byte buffer into samples, reusing the
// caller's slice so a capture callback allocates nothing per block.
func ReadFloat32(source []byte, into []float32) []float32 {
	count := len(source) / 4
	if cap(into) < count {
		into = make([]float32, count)
	}
	into = into[:count]
	for index := 0; index < count; index++ {
		into[index] = math.Float32frombits(binary.LittleEndian.Uint32(source[index*4:]))
	}
	return into
}

var (
	pointerMu    sync.Mutex
	pointerCache = map[string]unsafe.Pointer{}
)

// devicePointer hands miniaudio a device id.
//
// malgo allocates C memory every time DeviceID.Pointer is called and offers no
// way to release it, so the result is cached: one allocation per device for the
// life of the process rather than one per sound played.
func devicePointer(id malgo.DeviceID) unsafe.Pointer {
	pointerMu.Lock()
	defer pointerMu.Unlock()
	key := id.String()
	if pointer, ok := pointerCache[key]; ok {
		return pointer
	}
	pointer := id.Pointer()
	pointerCache[key] = pointer
	return pointer
}

// PlayTestTone plays a short tone on one device and blocks until it finishes.
//
// This is how the settings page lets her find her headphones: press Test on
// each device in turn until a tone comes out of the one she is wearing.
func PlayTestTone(device *Device, volume float64) error {
	const sampleRate = 48000
	_, err := Play(device, Tone(660.0, 0.35, sampleRate, volume), sampleRate, 1, nil)
	return err
}

// Pointer exposes a cached device id pointer for callers that open their own
// miniaudio devices, such as the microphone stream.
func Pointer(id malgo.DeviceID) unsafe.Pointer { return devicePointer(id) }
