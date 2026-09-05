package tts

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"

	"github.com/hajimehoshi/go-mp3"
)

// Audio is decoded PCM ready to be played: interleaved float32 samples.
type Audio struct {
	Samples    []float32
	SampleRate int
	Channels   int

	// Text is what was said. It is carried along so the speech log and the
	// tests can see what a buffer actually is.
	Text string
}

// AtVolume returns the same speech at a percentage of its own loudness, 100
// being untouched and 0 silence.
//
// Loudness is applied here, to the samples, rather than asked of the online
// voice that made them. The service would honour the asking; the reason not to
// ask is that the answer would then be part of what was rendered:
//
//   - The offline Windows voice has no volume to ask for, so a network that
//     drops used to take the volume setting with it.
//   - Loudness would belong to the cache. It used to be part of the cache key,
//     which meant every stored phrase was wrong the moment she changed the
//     volume, and every confirmation cost a round trip again until they had
//     all been re-rendered.
//   - It is a percentage of one sound rather than a percentage of everything.
//     The tones and the music are already scaled here; doing the voice
//     anywhere else would leave three volumes that mean three things.
//
// The cache is shared, so a scaled copy is made rather than the samples being
// changed in place. Full volume returns the audio untouched and copies
// nothing, which is the ordinary case.
func (a Audio) AtVolume(percent int) Audio {
	if percent >= 100 {
		return a
	}
	if percent < 0 {
		percent = 0
	}

	// Zero is silence rather than nothing: the same length of audio, played
	// and timed and interruptible exactly as it would be, only inaudible.
	// Dropping the samples instead would race the whole chat backlog through
	// in a second.
	gain := float32(percent) / 100
	scaled := make([]float32, len(a.Samples))
	for index, sample := range a.Samples {
		scaled[index] = sample * gain
	}
	a.Samples = scaled
	return a
}

// Duration is how long this audio takes to play.
func (a Audio) Duration() float64 {
	if a.SampleRate == 0 || a.Channels == 0 {
		return 0
	}
	return float64(len(a.Samples)) / float64(a.SampleRate*a.Channels)
}

// Frames is the number of sample frames, whatever the channel count.
func (a Audio) Frames() int {
	if a.Channels <= 0 {
		return len(a.Samples)
	}
	return len(a.Samples) / a.Channels
}

// Error is a synthesis or playback failure worth reporting aloud.
type Error struct{ Reason string }

func (e *Error) Error() string { return e.Reason }

func failure(format string, args ...any) error {
	return &Error{Reason: fmt.Sprintf(format, args...)}
}

// Decode turns MP3 or WAV bytes into PCM. Edge voices arrive as MP3, the
// offline Windows voice as WAV, and everything downstream only ever sees PCM.
func Decode(data []byte) (Audio, error) {
	if len(data) < 12 {
		return Audio{}, failure("could not decode audio: only %d bytes", len(data))
	}
	if bytes.Equal(data[0:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WAVE")) {
		return decodeWAV(data)
	}
	return decodeMP3(data)
}

func decodeMP3(data []byte) (Audio, error) {
	decoder, err := mp3.NewDecoder(bytes.NewReader(data))
	if err != nil {
		return Audio{}, failure("could not decode audio: %v", err)
	}
	raw, err := io.ReadAll(decoder)
	if err != nil {
		return Audio{}, failure("could not read decoded audio: %v", err)
	}
	// go-mp3 always produces 16-bit little-endian stereo.
	return Audio{
		Samples:    int16ToFloat32(raw),
		SampleRate: decoder.SampleRate(),
		Channels:   2,
	}, nil
}

// decodeWAV reads the subset of RIFF that the Windows speech synthesizer
// writes: one uncompressed PCM chunk, 8 or 16 bits, any rate.
func decodeWAV(data []byte) (Audio, error) {
	var (
		channels   int
		sampleRate int
		bits       int
		samples    []byte
		found      bool
	)

	for offset := 12; offset+8 <= len(data); {
		id := string(data[offset : offset+4])
		size := int(binary.LittleEndian.Uint32(data[offset+4 : offset+8]))
		body := offset + 8
		if body+size > len(data) {
			size = len(data) - body
		}
		switch id {
		case "fmt ":
			if size < 16 {
				return Audio{}, failure("could not decode audio: truncated WAV header")
			}
			channels = int(binary.LittleEndian.Uint16(data[body+2 : body+4]))
			sampleRate = int(binary.LittleEndian.Uint32(data[body+4 : body+8]))
			bits = int(binary.LittleEndian.Uint16(data[body+14 : body+16]))
		case "data":
			samples = data[body : body+size]
			found = true
		}
		offset = body + size
		if size%2 == 1 {
			offset++ // RIFF chunks are word aligned
		}
	}

	if !found || channels == 0 || sampleRate == 0 {
		return Audio{}, failure("could not decode audio: no usable WAV data")
	}
	switch bits {
	case 16:
		return Audio{Samples: int16ToFloat32(samples), SampleRate: sampleRate, Channels: channels}, nil
	case 8:
		converted := make([]float32, len(samples))
		for index, value := range samples {
			converted[index] = (float32(value) - 128.0) / 128.0
		}
		return Audio{Samples: converted, SampleRate: sampleRate, Channels: channels}, nil
	default:
		return Audio{}, failure("could not decode audio: %d-bit WAV is not supported", bits)
	}
}

func int16ToFloat32(raw []byte) []float32 {
	count := len(raw) / 2
	samples := make([]float32, count)
	for index := 0; index < count; index++ {
		value := int16(binary.LittleEndian.Uint16(raw[index*2:]))
		samples[index] = float32(value) / 32768.0
	}
	return samples
}

// TrimSilence strips the padding the online voices wrap around every phrase.
//
// Roughly a second of the measured duration is leading and trailing silence.
// Removing it is the difference between a confirmation that feels immediate
// and one that feels like the app is thinking about it.
func TrimSilence(audio Audio) Audio {
	if audio.Channels <= 0 || len(audio.Samples) == 0 {
		return audio
	}
	const threshold = 0.005
	const marginMS = 30.0

	frames := audio.Frames()
	first, last := -1, -1
	for frame := 0; frame < frames; frame++ {
		peak := float32(0)
		for channel := 0; channel < audio.Channels; channel++ {
			if value := abs32(audio.Samples[frame*audio.Channels+channel]); value > peak {
				peak = value
			}
		}
		if peak > threshold {
			if first < 0 {
				first = frame
			}
			last = frame
		}
	}
	if first < 0 {
		return audio // all quiet; leave it alone rather than return nothing
	}

	margin := int(float64(audio.SampleRate) * marginMS / 1000.0)
	start := max(0, first-margin)
	end := min(frames, last+margin)

	trimmed := audio
	trimmed.Samples = audio.Samples[start*audio.Channels : end*audio.Channels]
	return trimmed
}

func abs32(value float32) float32 {
	return float32(math.Abs(float64(value)))
}

var errNoAudio = errors.New("the voice returned no audio")
