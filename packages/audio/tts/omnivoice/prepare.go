package omnivoice

import (
	"encoding/binary"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"

	ort "github.com/yalue/onnxruntime_go"

	"github.com/exzork/mikkilens/packages/audio/onnx"
)

// Turning a recording into a voice is a separate job from speaking in one, and
// it is done once per recording rather than once per sentence.
//
// That matters because of what it costs. The encoder is 650 MB of weights --
// half again what the decoder and the language model together need at rest --
// and it is used to answer a question whose answer never changes: what codes
// does this wav file come out as. So it is loaded, asked, and closed, and the
// answer is written next to the recording as a .json. After that the 650 MB is
// never needed again on this machine, and the folder holds a voice rather than
// a recording of one.
//
// It also means a voice can be prepared somewhere else entirely and copied in.
// The .json is the voice; the .wav is only where it came from.

// Prepare turns data/models/omnivoice/voices/<name>.wav into <name>.json.
//
// A transcript in <name>.txt beside the recording is used if there is one, and
// is worth having: without it the model hears a voice saying something it was
// not told about, and pronunciation in the generated speech drifts towards
// whatever it guesses was said.
//
// It is safe to call for a voice that is already prepared, which is what makes
// it usable as "make sure this is ready" rather than something a caller has to
// know the state of.
func Prepare(name string) error {
	if name == "" {
		return &Error{Reason: "a voice needs a name"}
	}
	if exists(preparedPath(name)) {
		return nil
	}

	recording := recordingPath(name)
	if !exists(recording) {
		return failure("there is no recording for the voice %q; expected %s", name, recording)
	}
	if !EncoderInstalled() {
		return failure("the part of OmniVoice that takes a voice from a recording "+
			"is not installed; it goes in %s", onnxDir())
	}

	samples, err := readWAV(recording)
	if err != nil {
		return err
	}
	if len(samples) < SampleRate/2 {
		return failure("the recording for %q is shorter than half a second", name)
	}
	// Ten seconds is the model's own advice, and going past it costs twice:
	// the reference sits inside every prompt, so a long one makes every
	// sentence slower as well as cloning slightly worse.
	if limit := 10 * SampleRate; len(samples) > limit {
		samples = samples[:limit]
	}

	codes, err := encode(samples)
	if err != nil {
		return err
	}

	voice := Voice{
		Name:  name,
		Text:  readTranscript(name),
		RMS:   rootMeanSquare(samples),
		Codes: codes,
	}
	raw, err := json.MarshalIndent(voice, "", " ")
	if err != nil {
		return failure("could not write the voice %q: %v", name, err)
	}
	if err := os.MkdirAll(voiceDir(), 0o755); err != nil {
		return failure("could not write the voice %q: %v", name, err)
	}
	if err := os.WriteFile(preparedPath(name), raw, 0o644); err != nil {
		return failure("could not write the voice %q: %v", name, err)
	}
	return nil
}

func readTranscript(name string) string {
	raw, err := os.ReadFile(transcriptPath(name))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// encode opens the encoder, asks it one question and closes it again.
func encode(samples []float32) ([][]int32, error) {
	if err := onnx.Start(); err != nil {
		return nil, &Error{Reason: err.Error()}
	}

	options, err := onnx.Options(4)
	if err != nil {
		return nil, &Error{Reason: err.Error()}
	}
	defer options.Destroy()

	session, err := ort.NewDynamicAdvancedSession(
		filepath.Join(onnxDir(), "audio_encoder.onnx"),
		[]string{"waveform"}, []string{"audio_codes"}, options)
	if err != nil {
		return nil, failure("could not load the voice encoder: %v", err)
	}
	defer session.Destroy()

	waveform, err := ort.NewTensor(ort.NewShape(1, 1, int64(len(samples))), samples)
	if err != nil {
		return nil, failure("could not prepare the recording: %v", err)
	}
	defer waveform.Destroy()

	outputs := []ort.Value{nil}
	if err := session.Run([]ort.Value{waveform}, outputs); err != nil {
		return nil, failure("could not read the recording: %v", err)
	}
	defer func() {
		if outputs[0] != nil {
			_ = outputs[0].Destroy()
		}
	}()

	tensor, ok := outputs[0].(*ort.Tensor[int32])
	if !ok {
		return nil, failure("the voice encoder returned %T rather than codes", outputs[0])
	}
	shape := tensor.GetShape()
	if len(shape) != 3 || shape[1] != Codebooks {
		return nil, failure("the voice encoder returned codes shaped %v", shape)
	}

	frames := int(shape[2])
	if frames == 0 {
		return nil, &Error{Reason: "the recording produced no audio codes"}
	}
	data := tensor.GetData()

	codes := make([][]int32, Codebooks)
	for row := range codes {
		codes[row] = append([]int32(nil), data[row*frames:(row+1)*frames]...)
	}
	return codes, nil
}

// rootMeanSquare is how loud the recording is, which is what the generated
// speech is matched back to. Peak would be the wrong measure: one clipped
// consonant would decide the level for a whole voice.
func rootMeanSquare(samples []float32) float32 {
	if len(samples) == 0 {
		return 0
	}
	total := 0.0
	for _, value := range samples {
		total += float64(value) * float64(value)
	}
	return float32(math.Sqrt(total / float64(len(samples))))
}

// -- reading a recording -------------------------------------------------------

// readWAV reads the subset of RIFF that a recording is likely to be, as mono
// float32 at the model's own rate.
//
// Deliberately narrow. This reads a file somebody exported from Audacity or
// recorded on a phone, not an arbitrary RIFF stream, and the failure it has to
// avoid is not "this obscure chunk layout is unsupported" but "this loaded and
// the voice sounds like a chipmunk" -- which is what a sample rate quietly
// ignored sounds like.
func readWAV(path string) ([]float32, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, failure("could not read %s: %v", filepath.Base(path), err)
	}
	if len(raw) < 44 || string(raw[0:4]) != "RIFF" || string(raw[8:12]) != "WAVE" {
		return nil, failure("%s is not a WAV file", filepath.Base(path))
	}

	var (
		format, channels, bits int
		rate                   int
		data                   []byte
	)
	for at := 12; at+8 <= len(raw); {
		id := string(raw[at : at+4])
		size := int(binary.LittleEndian.Uint32(raw[at+4 : at+8]))
		body := at + 8
		if size < 0 || body+size > len(raw) {
			size = len(raw) - body
		}
		switch id {
		case "fmt ":
			if size < 16 {
				return nil, failure("%s has a truncated format block", filepath.Base(path))
			}
			format = int(binary.LittleEndian.Uint16(raw[body : body+2]))
			channels = int(binary.LittleEndian.Uint16(raw[body+2 : body+4]))
			rate = int(binary.LittleEndian.Uint32(raw[body+4 : body+8]))
			bits = int(binary.LittleEndian.Uint16(raw[body+14 : body+16]))
		case "data":
			data = raw[body : body+size]
		}
		at = body + size
		if size%2 == 1 {
			at++ // RIFF chunks are word aligned
		}
	}

	if len(data) == 0 || channels < 1 {
		return nil, failure("%s has no audio in it", filepath.Base(path))
	}
	// 0xFFFE is "extensible", whose real format is named further into the
	// block; in practice it is PCM or float, and bits tells them apart.
	if format != 1 && format != 3 && format != 0xFFFE {
		return nil, failure("%s is compressed; save it as plain PCM WAV", filepath.Base(path))
	}

	samples, err := decodeSamples(data, bits, format, channels)
	if err != nil {
		return nil, failure("%s: %v", filepath.Base(path), err)
	}
	if rate != SampleRate {
		samples = resample(samples, rate, SampleRate)
	}
	return samples, nil
}

// decodeSamples turns interleaved frames into mono float32, averaging the
// channels rather than taking the first: a recording made on a stereo
// interface often has the voice on one side only, and taking the wrong side
// gives silence.
func decodeSamples(data []byte, bits, format, channels int) ([]float32, error) {
	width := bits / 8
	if width == 0 {
		return nil, &Error{Reason: "the recording does not say how deep its samples are"}
	}
	frames := len(data) / (width * channels)
	if frames == 0 {
		return nil, &Error{Reason: "the recording has no complete frames in it"}
	}

	samples := make([]float32, frames)
	for frame := 0; frame < frames; frame++ {
		total := float32(0)
		for channel := 0; channel < channels; channel++ {
			at := (frame*channels + channel) * width
			value, err := oneSample(data[at:at+width], bits, format)
			if err != nil {
				return nil, err
			}
			total += value
		}
		samples[frame] = total / float32(channels)
	}
	return samples, nil
}

func oneSample(raw []byte, bits, format int) (float32, error) {
	switch {
	case format == 3 && bits == 32:
		return math.Float32frombits(binary.LittleEndian.Uint32(raw)), nil
	case format == 3 && bits == 64:
		return float32(math.Float64frombits(binary.LittleEndian.Uint64(raw))), nil
	case bits == 8:
		// Eight-bit PCM is unsigned, alone among the depths.
		return (float32(raw[0]) - 128) / 128, nil
	case bits == 16:
		return float32(int16(binary.LittleEndian.Uint16(raw))) / 32768, nil
	case bits == 24:
		value := int32(raw[0]) | int32(raw[1])<<8 | int32(raw[2])<<16
		if value&0x800000 != 0 {
			value |= ^0xFFFFFF // sign-extend into the fourth byte
		}
		return float32(value) / 8388608, nil
	case bits == 32:
		return float32(int32(binary.LittleEndian.Uint32(raw))) / 2147483648, nil
	default:
		return 0, failure("%d-bit samples are not supported", bits)
	}
}

// resample is linear interpolation, which is not the best way to change a
// sample rate but is an honest one for this job: the result is fed to an
// encoder that turns ten seconds of audio into eighty numbers a second, and
// the artefacts a better filter would remove do not survive that.
func resample(samples []float32, from, to int) []float32 {
	if from <= 0 || from == to || len(samples) == 0 {
		return samples
	}
	wanted := int(float64(len(samples)) * float64(to) / float64(from))
	if wanted < 1 {
		return samples
	}

	out := make([]float32, wanted)
	ratio := float64(from) / float64(to)
	for index := range out {
		at := float64(index) * ratio
		left := int(at)
		if left >= len(samples)-1 {
			out[index] = samples[len(samples)-1]
			continue
		}
		fraction := float32(at - float64(left))
		out[index] = samples[left]*(1-fraction) + samples[left+1]*fraction
	}
	return out
}
