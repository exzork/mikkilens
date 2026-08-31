package devices

import (
	"sync/atomic"
	"time"

	"github.com/exzork/mikkilens/packages/audio"
	"github.com/exzork/mikkilens/packages/audio/wasapi"
)

// Playback opens a stream per sound rather than holding one open for the life
// of the process. A shared-mode open costs a few milliseconds, which is
// nothing beside the second a synthesized sentence takes, and it means a
// device that disappears mid-stream -- a headset unplugged, a monitor turned
// off -- costs one failed sound rather than every sound afterwards.

// SetLeadIn sets how much silence precedes a sound on a device that has gone
// idle, which is what stops a Bluetooth headset swallowing the first word.
func SetLeadIn(duration time.Duration) { wasapi.SetLeadIn(duration) }

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

	id := ""
	if device != nil {
		id = device.ID
	}
	return wasapi.Play(id, samples, sampleRate, channels, asInterrupt(interrupt))
}

// asInterrupt avoids handing WASAPI a non-nil interface wrapping a nil
// pointer, which would look like an interrupt that never fires but is still
// checked on every block.
func asInterrupt(interrupt *Interrupt) wasapi.Interrupt {
	if interrupt == nil {
		return nil
	}
	return interrupt
}

func waitSilently(frames, sampleRate int, interrupt *Interrupt) bool {
	if sampleRate <= 0 {
		return true
	}
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

// PlayTestTone plays a short tone on one device and blocks until it finishes.
//
// This is how the settings app lets her find her headphones: press Test on
// each device in turn until a tone comes out of the one she is wearing.
func PlayTestTone(device *Device, volume float64) error {
	const sampleRate = 48000
	_, err := Play(device, Tone(660.0, 0.35, sampleRate, volume), sampleRate, 1, nil)
	return err
}
