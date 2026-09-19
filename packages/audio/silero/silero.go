// Package silero tells speech apart from everything else in a recording.
//
// Whisper writes something down for any audio it is handed, speech or not. Two
// seconds of room tone, a fan or a game's music comes back as "Terima kasih
// kerana menonton" -- a line it learned from the end of YouTube videos -- and
// MikkiLens then says out loud that it does not know that command. The
// recorder's own voice detector is a loudness-and-spectrum check tuned to
// start recording promptly, which is the right job for it and the wrong one
// for this: plenty of noise passes it.
//
// Silero VAD is a small network trained on exactly this question, speech or
// not, and it answers it far better than any threshold. It runs here on each
// recording before recognition does, on the ONNX runtime the wake word already
// loads, and a recording with almost no speech in it never reaches Whisper.
//
// The model is Silero VAD v5 (MIT licence, github.com/snakers4/silero-vad),
// two megabytes, inside the executable.
package silero

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/exzork/mikkilens/packages/audio/onnx"
	"github.com/exzork/mikkilens/packages/core/paths"
	ort "github.com/yalue/onnxruntime_go"
)

//go:embed silero_vad.onnx
var model []byte

const (
	// SampleRate is the only rate this is asked at: the recorder's.
	SampleRate = 16000

	// window is how much new audio each step looks at, and context how much
	// of the previous window it is given again. Both are fixed by the model
	// at 16 kHz.
	window  = 512
	context = 64

	// threshold is the speech probability above which a window counts. The
	// model's own recommended value.
	threshold = 0.5
)

// Detector runs the model. One is enough for the whole program; calls are
// serialised, and each recording is well under a second of work.
type Detector struct {
	mu      sync.Mutex
	session *ort.DynamicAdvancedSession
	err     error
	once    sync.Once
}

// Default is the detector the engine uses.
var Default = &Detector{}

func (d *Detector) load() {
	d.once.Do(func() {
		if err := onnx.Start(); err != nil {
			d.err = err
			return
		}
		path, err := install()
		if err != nil {
			d.err = err
			return
		}
		// One thread: this runs while she is streaming, and the answer is
		// needed in tens of milliseconds, not in the fewest possible.
		options, err := onnx.Options(1)
		if err != nil {
			d.err = err
			return
		}
		defer options.Destroy()
		d.session, d.err = ort.NewDynamicAdvancedSession(path,
			[]string{"input", "state", "sr"}, []string{"output", "stateN"}, options)
	})
}

// install writes the model beside the others, because the runtime loads from
// a path. Rewritten only when missing or a different size.
func install() (string, error) {
	target := filepath.Join(paths.ModelsDir(), "silero_vad.onnx")
	if info, err := os.Stat(target); err == nil && info.Size() == int64(len(model)) {
		return target, nil
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", err
	}
	temporary := target + ".part"
	if err := os.WriteFile(temporary, model, 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(temporary, target); err != nil {
		_ = os.Remove(temporary)
		return "", err
	}
	return target, nil
}

// SpeechSeconds is how much of a 16 kHz mono recording is speech.
//
// An error means the question could not be asked -- no runtime on this
// machine, most often -- and the caller should carry on as it did before this
// existed rather than refuse to listen.
func (d *Detector) SpeechSeconds(audio []float32) (float64, error) {
	d.load()
	if d.err != nil {
		return 0, d.err
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	state := make([]float32, 2*1*128)
	previous := make([]float32, context)
	input := make([]float32, context+window)

	rate, err := ort.NewScalar(int64(SampleRate))
	if err != nil {
		return 0, err
	}
	defer rate.Destroy()

	speech := 0
	for start := 0; start+window <= len(audio); start += window {
		copy(input, previous)
		copy(input[context:], audio[start:start+window])
		copy(previous, audio[start+window-context:start+window])

		probability, next, err := d.step(input, state, rate)
		if err != nil {
			return 0, err
		}
		state = next
		if probability >= threshold {
			speech++
		}
	}
	return float64(speech*window) / SampleRate, nil
}

func (d *Detector) step(input, state []float32, rate ort.Value) (float32, []float32, error) {
	inputTensor, err := ort.NewTensor(ort.NewShape(1, int64(len(input))), input)
	if err != nil {
		return 0, nil, err
	}
	defer inputTensor.Destroy()
	stateTensor, err := ort.NewTensor(ort.NewShape(2, 1, 128), state)
	if err != nil {
		return 0, nil, err
	}
	defer stateTensor.Destroy()

	outputs := []ort.Value{nil, nil}
	if err := d.session.Run([]ort.Value{inputTensor, stateTensor, rate}, outputs); err != nil {
		return 0, nil, err
	}
	defer func() {
		for _, output := range outputs {
			if output != nil {
				_ = output.Destroy()
			}
		}
	}()

	probability, ok := outputs[0].(*ort.Tensor[float32])
	if !ok {
		return 0, nil, fmt.Errorf("the speech detector returned %T", outputs[0])
	}
	next, ok := outputs[1].(*ort.Tensor[float32])
	if !ok {
		return 0, nil, fmt.Errorf("the speech detector returned %T for its state", outputs[1])
	}
	return probability.GetData()[0], append([]float32(nil), next.GetData()...), nil
}
