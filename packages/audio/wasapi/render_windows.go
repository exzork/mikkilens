//go:build windows

package wasapi

import (
	"runtime"
	"time"
	"unsafe"
)

// Interrupt cuts a sound short from another goroutine. It is what lets an
// error preempt a chat message mid-word.
type Interrupt interface{ Stopped() bool }

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

	if err := client.start(); err != nil {
		return false, err
	}
	defer client.stop()

	// A poll interval well under the buffer length keeps the device fed
	// without spinning.
	interval := time.Duration(client.frames) * time.Second /
		time.Duration(client.sampleRate()) / 4
	if interval < 2*time.Millisecond {
		interval = 2 * time.Millisecond
	}

	position := 0
	for position < len(samples) {
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

		remaining := uint32((len(samples) - position) / channels)
		wanted := available
		if remaining < wanted {
			wanted = remaining
		}
		if wanted == 0 {
			break
		}

		var buffer unsafe.Pointer
		hr := call(render, 3, uintptr(wanted), uintptr(unsafe.Pointer(&buffer))) // GetBuffer
		if err := check("IAudioRenderClient::GetBuffer", hr); err != nil {
			return false, err
		}

		count := int(wanted) * channels
		copy(unsafe.Slice((*float32)(buffer), count), samples[position:position+count])
		position += count

		hr = call(render, 4, uintptr(wanted), 0) // ReleaseBuffer
		if err := check("IAudioRenderClient::ReleaseBuffer", hr); err != nil {
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
