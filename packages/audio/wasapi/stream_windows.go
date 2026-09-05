//go:build windows

package wasapi

import (
	"errors"
	"io"
	"runtime"
	"time"
	"unsafe"
)

// Playing something that is still arriving.
//
// [Play] takes a finished buffer and is right for everything MikkiLens says:
// a sentence is a second of audio, synthesized whole before a word of it is
// heard. A song is four minutes, arrives over the network, and would be
// ninety megabytes of float32 if it were assembled first -- on a machine that
// is also encoding video, and that has been asked to stay under four
// gigabytes.
//
// So this pulls instead. The device asks for the next block when it needs one,
// and whatever is feeding it has until then to produce it.

// Source produces audio on demand.
//
// Format is called once, after the device is open, with what the device
// actually accepted -- which is not always what was asked for. Handing the
// negotiation to the source rather than resampling here is deliberate: the
// thing on the other end of this is ffmpeg, which resamples properly, against
// a converter here that would have to stitch across every block boundary and
// leave a seam in the music every ten milliseconds.
type Source interface {
	Format(sampleRate, channels int) error

	// Read fills dst with interleaved samples and returns how many it wrote.
	// io.EOF means the sound has ended; a short read that is not EOF means
	// nothing has arrived yet, and is padded with silence rather than treated
	// as the end.
	Read(dst []float32) (int, error)
}

// Control is how a running stream is steered from another goroutine.
//
// Read on every block, never cached, because all three change while the sound
// is playing: she stops it, she pauses it, or MikkiLens starts speaking and
// the music has to get out of the way.
type Control interface {
	Stopped() bool
	Paused() bool

	// Gain multiplies every sample. One is untouched; ducking under the voice
	// is a fraction. Applied here rather than by the source so that it takes
	// effect on the next block rather than on whatever has already been
	// buffered ahead.
	Gain() float32
}

// stallPadding is how long a source may produce nothing before the stream is
// given up on.
//
// Silence is written meanwhile, so a network hiccup is a gap rather than the
// end of the song. Past this it is not a hiccup, and standing in silence
// waiting for a stream that is not coming back is worse than being told.
const stallPadding = 20 * time.Second

// Stream plays a source until it ends, is stopped, or stalls.
func Stream(deviceID string, source Source, sampleRate, channels int, control Control) error {
	if source == nil {
		return errors.New("nothing to play")
	}
	if channels < 1 {
		channels = 1
	}

	done := make(chan error, 1)
	go func() {
		// One locked thread for the life of the stream: COM apartments belong
		// to threads, and a stream that migrates mid-playback fails in ways
		// that are very hard to reproduce.
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		scope, err := enterCOM()
		if err != nil {
			done <- err
			return
		}
		defer scope.leave()

		done <- stream(deviceID, source, sampleRate, channels, control)
	}()
	return <-done
}

func stream(deviceID string, source Source, sampleRate, channels int, control Control) error {
	enumerator, err := createEnumerator()
	if err != nil {
		return err
	}
	defer release(enumerator)

	device, err := openDevice(enumerator, deviceID, Render)
	if err != nil {
		return err
	}

	client, err := activate(device, Render, sampleRate, channels)
	if err != nil {
		release(device)
		return err
	}
	defer client.close()

	if !client.isFloat32() {
		return &Error{Op: "unsupported device format", HResult: 0}
	}

	// What the driver actually gave us, which the source now has to produce.
	channels = client.channels()
	if err := source.Format(client.sampleRate(), channels); err != nil {
		return err
	}

	render, err := client.service(&iidIAudioRenderClient)
	if err != nil {
		return err
	}
	defer release(render)

	interval := time.Duration(client.frames) * time.Second /
		time.Duration(client.sampleRate()) / 4
	if interval < 2*time.Millisecond {
		interval = 2 * time.Millisecond
	}

	// Staging buffer, sized to the whole device buffer so one Read can fill
	// whatever is asked for.
	staging := make([]float32, int(client.frames)*channels)
	silence := leadInFrames(client.sampleRate())
	defer markPlayed()

	var (
		ended    bool
		lastGood = time.Now()
	)

	// writeChunk hands one block to the device: the lead-in silence first,
	// then whatever the source has, then silence for anything left over.
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

		// Paused means silence rather than a stopped device: the stream stays
		// open, the source is not read from, and resuming picks up exactly
		// where it left off. Reading on and throwing it away would skip
		// through the song while she was not listening.
		paused := control != nil && control.Paused()

		if !paused && !ended && written < len(destination) {
			wanted := destination[written:]
			read, readErr := source.Read(staging[:len(wanted)])
			if read > 0 {
				gain := float32(1)
				if control != nil {
					gain = control.Gain()
				}
				if gain == 1 {
					copy(wanted, staging[:read])
				} else {
					for index := 0; index < read; index++ {
						wanted[index] = staging[index] * gain
					}
				}
				written += read
				lastGood = time.Now()
			}
			if errors.Is(readErr, io.EOF) {
				ended = true
			} else if readErr != nil {
				return readErr
			}
		}

		for ; written < len(destination); written++ {
			destination[written] = 0
		}

		hr = call(render, 4, uintptr(frames), 0) // ReleaseBuffer
		return check("IAudioRenderClient::ReleaseBuffer", hr)
	}

	// Fill before starting. WASAPI renders whatever is in the buffer the
	// instant it is started, so starting empty makes the device's first act an
	// underrun -- a click, and the first note clipped.
	if err := writeChunk(client.frames); err != nil {
		return err
	}
	if err := client.start(); err != nil {
		return err
	}
	defer client.stop()

	for {
		if control != nil && control.Stopped() {
			return nil
		}

		padding, err := client.padding()
		if err != nil {
			return err
		}

		// Everything handed over has been heard and there is no more coming.
		if ended && padding == 0 {
			return nil
		}

		available := client.frames - padding
		if available == 0 {
			time.Sleep(interval)
			continue
		}
		if err := writeChunk(available); err != nil {
			return err
		}

		// A source producing nothing for this long is not buffering any more.
		// Paused does not count: that is her decision, not a fault.
		if !ended && (control == nil || !control.Paused()) &&
			time.Since(lastGood) > stallPadding {
			return &Error{Op: "the audio stopped arriving", HResult: 0}
		}
	}
}
