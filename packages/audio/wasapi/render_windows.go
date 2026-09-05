//go:build windows

package wasapi

import (
	"runtime"
	"sync"
	"time"
	"unsafe"
)

// Interrupt cuts a sound short from another goroutine. It is what lets an
// error preempt a chat message mid-word.
type Interrupt interface{ Stopped() bool }

// A sleeping output device swallows the beginning of a sound.
//
// Bluetooth headphones drop the audio link when nothing is playing and take a
// few hundred milliseconds to bring it back up. Anything sent during that
// window is simply lost -- and losing the first half second of a sentence
// loses the word that carries it, which when the answer only ever arrives by
// ear is the whole message. The tail of a sound was already waited out for the
// same reason; this is the other end of it.
//
// The fix is to send silence first and let that be what the wake-up eats.
var (
	leadInMu   sync.Mutex
	leadIn     = 300 * time.Millisecond
	lastPlayed time.Time
)

// warmWindow is how long a device is assumed to still be awake after playing.
//
// Within it the lead-in is skipped, because a sound following closely on
// another finds the link already up and delaying it would add latency for
// nothing. Guessing short is the safe direction: the cost is a little silence
// before a sound that did not need it, against a clipped first word.
const warmWindow = 2 * time.Second

// SetLeadIn sets how much silence precedes a sound on a cold device. Zero
// disables it, which is right for anything wired.
func SetLeadIn(duration time.Duration) {
	if duration < 0 {
		duration = 0
	}
	leadInMu.Lock()
	defer leadInMu.Unlock()
	leadIn = duration
}

// leadInFrames is how much silence this sound should start with, in frames.
func leadInFrames(sampleRate int) uint32 {
	leadInMu.Lock()
	defer leadInMu.Unlock()

	if leadIn <= 0 {
		return 0
	}
	if !lastPlayed.IsZero() && time.Since(lastPlayed) < warmWindow {
		return 0 // still awake from the last sound
	}
	return uint32(leadIn.Seconds() * float64(sampleRate))
}

// markPlayed records that the device was in use just now.
func markPlayed() {
	leadInMu.Lock()
	defer leadInMu.Unlock()
	lastPlayed = time.Now()
}

// Play writes interleaved float32 samples to one endpoint and blocks until
// they have been heard.
//
// It returns false when the interrupt cut it short. An empty device id means
// whatever Windows has set as the default.
func Play(deviceID string, samples []float32, sampleRate, channels int, interrupt Interrupt) (bool, error) {
	if len(samples) == 0 {
		return true, nil
	}
	if channels < 1 {
		channels = 1
	}

	type answer struct {
		completed bool
		err       error
	}
	done := make(chan answer, 1)

	go func() {
		// The whole stream lives on one locked thread: COM apartments belong
		// to threads, and a stream that migrates mid-playback fails in ways
		// that are very hard to reproduce.
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		scope, err := enterCOM()
		if err != nil {
			done <- answer{err: err}
			return
		}
		defer scope.leave()

		completed, err := play(deviceID, samples, sampleRate, channels, interrupt)
		done <- answer{completed: completed, err: err}
	}()

	result := <-done
	return result.completed, result.err
}

func play(deviceID string, samples []float32, sampleRate, channels int, interrupt Interrupt) (bool, error) {
	enumerator, err := createEnumerator()
	if err != nil {
		return false, err
	}
	defer release(enumerator)

	device, err := openDevice(enumerator, deviceID, Render)
	if err != nil {
		return false, err
	}

	client, err := activate(device, Render, sampleRate, channels)
	if err != nil {
		release(device)
		return false, err
	}
	defer client.close()

	// If the driver refused our format, convert to what it did accept rather
	// than going silent.
	if client.sampleRate() != sampleRate || client.channels() != channels {
		samples = convert(samples, sampleRate, channels, client.sampleRate(), client.channels())
		channels = client.channels()
	}
	if !client.isFloat32() {
		return false, &Error{Op: "unsupported device format", HResult: 0}
	}

	render, err := client.service(&iidIAudioRenderClient)
	if err != nil {
		return false, err
	}
	defer release(render)

	// A poll interval well under the buffer length keeps the device fed
	// without spinning.
	interval := time.Duration(client.frames) * time.Second /
		time.Duration(client.sampleRate()) / 4
	if interval < 2*time.Millisecond {
		interval = 2 * time.Millisecond
	}

	// The sound is treated as silence followed by the samples, so the lead-in
	// needs no copy of the audio and costs nothing when it is zero.
	silence := leadInFrames(client.sampleRate())
	position := 0
	defer markPlayed()

	pending := func() uint32 {
		return silence + uint32((len(samples)-position)/channels)
	}

	// writeChunk hands one block to the device, drawing silence first and then
	// audio, and padding anything left over at the end of the sound.
	writeChunk := func(frames uint32) error {
		var buffer unsafe.Pointer
		hr := call(render, 3, uintptr(frames), uintptr(unsafe.Pointer(&buffer))) // GetBuffer
		if err := check("IAudioRenderClient::GetBuffer", hr); err != nil {
			return err
		}
		destination := unsafe.Slice((*float32)(buffer), int(frames)*channels)

		written := 0
		for silence > 0 && written+channels <= len(destination) {
			for channel := 0; channel < channels; channel++ {
				destination[written+channel] = 0
			}
			written += channels
			silence--
		}
		if written < len(destination) && position < len(samples) {
			copied := copy(destination[written:], samples[position:])
			position += copied
			written += copied
		}
		for ; written < len(destination); written++ {
			destination[written] = 0
		}

		hr = call(render, 4, uintptr(frames), 0) // ReleaseBuffer
		return check("IAudioRenderClient::ReleaseBuffer", hr)
	}

	// Fill the buffer before starting, not after. WASAPI renders whatever is
	// in the buffer the instant it is started, so starting empty makes the
	// device's first act an underrun -- a click, or a clipped first syllable.
	if prefill := min(client.frames, pending()); prefill > 0 {
		if err := writeChunk(prefill); err != nil {
			return false, err
		}
	}

	if err := client.start(); err != nil {
		return false, err
	}
	defer client.stop()

	for pending() > 0 {
		if interrupt != nil && interrupt.Stopped() {
			return false, nil
		}

		padding, err := client.padding()
		if err != nil {
			return false, err
		}
		available := client.frames - padding
		if available == 0 {
			time.Sleep(interval)
			continue
		}

		wanted := min(available, pending())
		if wanted == 0 {
			break
		}
		if err := writeChunk(wanted); err != nil {
			return false, err
		}
	}

	// Wait for the tail to actually leave the device, or the last word is cut
	// off -- which on a confirmation is the word that mattered.
	for {
		if interrupt != nil && interrupt.Stopped() {
			return false, nil
		}
		padding, err := client.padding()
		if err != nil || padding == 0 {
			return true, nil
		}
		time.Sleep(interval)
	}
}

// convert resamples and re-channels audio for a driver that refused our
// format. Linear interpolation is enough here: it only runs on the fallback
// path, and only for speech.
func convert(samples []float32, fromRate, fromChannels, toRate, toChannels int) []float32 {
	if fromChannels < 1 {
		fromChannels = 1
	}
	frames := len(samples) / fromChannels
	if frames == 0 {
		return nil
	}

	outFrames := frames
	if fromRate != toRate && fromRate > 0 {
		outFrames = int(float64(frames) * float64(toRate) / float64(fromRate))
	}
	out := make([]float32, outFrames*toChannels)

	for frame := 0; frame < outFrames; frame++ {
		source := float64(frame) * float64(frames-1) / float64(max(1, outFrames-1))
		low := int(source)
		high := min(low+1, frames-1)
		fraction := float32(source - float64(low))

		for channel := 0; channel < toChannels; channel++ {
			// Extra output channels repeat the last input one, which turns
			// mono into stereo correctly and never leaves a channel silent.
			from := min(channel, fromChannels-1)
			a := samples[low*fromChannels+from]
			b := samples[high*fromChannels+from]
			out[frame*toChannels+channel] = a + (b-a)*fraction
		}
	}
	return out
}
