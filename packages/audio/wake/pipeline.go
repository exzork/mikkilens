package wake

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	ort "github.com/yalue/onnxruntime_go"

	"github.com/exzork/mikkilens/packages/audio/assets"
	"github.com/exzork/mikkilens/packages/core/paths"
)

// The openWakeWord pipeline runs in three stages, and the rates between them
// are what the buffering here is built around:
//
//	1280 audio samples (80 ms)  ->  8 mel frames  ->  1 embedding
//	the last 16 embeddings (1.28 s)  ->  one score
//
// So one chunk of audio produces exactly one score, and the classifier always
// sees the same 1.28 seconds of context openWakeWord trained it on.
const (
	melBins       = 32
	melHop        = 160 // samples per mel frame
	melWindow     = 76  // mel frames the embedding model consumes
	melContext    = 480 // extra left context, so new frames have history
	embeddingSize = 96
	featureWindow = 16 // embeddings the classifier consumes

	melFramesPerChunk = ChunkSamples / melHop // 8
)

var (
	runtimeOnce sync.Once
	runtimeErr  error
)

// initRuntime loads the ONNX runtime shared library.
//
// It is a separate download rather than something linked in, because the build
// that suits her machine (CPU, CUDA, DirectML) is her choice, and shipping one
// would be shipping the wrong one.
func initRuntime() error {
	runtimeOnce.Do(func() {
		if ort.IsInitialized() {
			return
		}
		library, err := findRuntimeLibrary()
		if err != nil {
			runtimeErr = err
			return
		}
		ort.SetSharedLibraryPath(library)
		if err := ort.InitializeEnvironment(); err != nil {
			runtimeErr = &Error{Reason: startupReason(library, err)}
		}
	})
	return runtimeErr
}

// startupReason turns the runtime's own refusal into something worth hearing.
//
// The common failure is not a corrupt file but the wrong version: the engine is
// compiled against one ORT C API, and an older library answers a different one
// and refuses with "Error setting ORT API base". Said as-is that names neither
// the file at fault nor anything to do about it, which for someone who cannot
// glance at a folder is the difference between a fixable problem and a wake
// word that has simply stopped working.
//
// The file is named rather than deleted here. Removing something of hers on her
// behalf, at startup, because a library disagreed about a version number, is
// not a decision this code should be making on its own.
func startupReason(library string, err error) string {
	reason := "the ONNX runtime could not start: " + err.Error()
	if !strings.Contains(err.Error(), "ORT API base") {
		return reason
	}
	return fmt.Sprintf("%s in %s is not the version MikkiLens needs (%s). "+
		"Delete it and start again to fetch the right one; the hotkey works meanwhile.",
		filepath.Base(library), filepath.Dir(library), assets.RuntimeVersion)
}

func findRuntimeLibrary() (string, error) {
	names := []string{"onnxruntime.dll", "libonnxruntime.so", "libonnxruntime.dylib"}
	directories := []string{
		paths.ModelsDir(),
		filepath.Join(paths.ModelsDir(), "onnxruntime"),
		filepath.Join(paths.Root(), "vendor", "onnxruntime"),
		paths.Root(),
	}
	for _, directory := range directories {
		for _, name := range names {
			candidate := filepath.Join(directory, name)
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				return candidate, nil
			}
		}
	}
	return "", &Error{Reason: "onnxruntime.dll was not found; put it in data/models to " +
		"use a wake word. The hotkey works without it."}
}

// model wraps one ONNX session with the input and output names the file
// itself declares.
type model struct {
	session *ort.DynamicAdvancedSession
	input   string
	output  string
}

func loadModel(path string) (*model, error) {
	// The names come from the model rather than being hard-coded: openWakeWord
	// has used several over the years, and guessing them is how this breaks
	// silently rather than loudly.
	inputs, outputs, err := ort.GetInputOutputInfo(path)
	if err != nil {
		return nil, &Error{Reason: fmt.Sprintf("could not read %s: %v", filepath.Base(path), err)}
	}
	if len(inputs) == 0 || len(outputs) == 0 {
		return nil, &Error{Reason: filepath.Base(path) + " has no usable inputs or outputs"}
	}

	options, err := modestOptions()
	if err != nil {
		return nil, err
	}
	defer options.Destroy()

	session, err := ort.NewDynamicAdvancedSession(
		path, []string{inputs[0].Name}, []string{outputs[0].Name}, options)
	if err != nil {
		return nil, &Error{Reason: fmt.Sprintf("could not load %s: %v", filepath.Base(path), err)}
	}
	return &model{session: session, input: inputs[0].Name, output: outputs[0].Name}, nil
}

// modestOptions keep the runtime from taking the whole machine.
//
// By default ONNX Runtime sizes a thread pool to the core count for every
// session, and those threads spin rather than sleep while waiting for work.
// Three sessions of that pegged every core on this machine and made typing lag
// in other applications -- on a box that is also encoding video, which is the
// one thing MikkiLens must never disturb.
//
// These models are tiny: one thread scores a chunk in about six milliseconds,
// against the eighty milliseconds of audio it represents. There is nothing for
// a pool to do but burn power.
func modestOptions() (*ort.SessionOptions, error) {
	options, err := ort.NewSessionOptions()
	if err != nil {
		return nil, &Error{Reason: "could not configure the ONNX runtime: " + err.Error()}
	}

	failed := func(err error) (*ort.SessionOptions, error) {
		options.Destroy()
		return nil, &Error{Reason: "could not configure the ONNX runtime: " + err.Error()}
	}

	if err := options.SetIntraOpNumThreads(1); err != nil {
		return failed(err)
	}
	if err := options.SetInterOpNumThreads(1); err != nil {
		return failed(err)
	}
	if err := options.SetExecutionMode(ort.ExecutionModeSequential); err != nil {
		return failed(err)
	}

	// Spinning is what actually burns the cores. It is a performance knob for
	// servers running back-to-back batches, and the opposite of what a
	// background listener wants.
	for key, value := range map[string]string{
		"session.intra_op.allow_spinning": "0",
		"session.inter_op.allow_spinning": "0",
	} {
		if err := options.AddSessionConfigEntry(key, value); err != nil {
			// An older runtime may not know the key. Not worth failing over:
			// the thread limits above already do most of the work.
			slog.Debug("the ONNX runtime did not accept a setting", "key", key, "error", err)
		}
	}
	return options, nil
}

// run feeds one float32 tensor through and returns the flattened output.
func (m *model) run(shape ort.Shape, data []float32) ([]float32, error) {
	input, err := ort.NewTensor(shape, data)
	if err != nil {
		return nil, err
	}
	defer input.Destroy()

	// A nil output lets onnxruntime allocate whatever shape the model
	// produces, which keeps this working across openWakeWord's revisions.
	outputs := []ort.Value{nil}
	if err := m.session.Run([]ort.Value{input}, outputs); err != nil {
		return nil, err
	}
	defer func() {
		if outputs[0] != nil {
			_ = outputs[0].Destroy()
		}
	}()

	tensor, ok := outputs[0].(*ort.Tensor[float32])
	if !ok {
		return nil, fmt.Errorf("the model returned %T rather than float32", outputs[0])
	}
	return append([]float32(nil), tensor.GetData()...), nil
}

func (m *model) close() {
	if m != nil && m.session != nil {
		_ = m.session.Destroy()
	}
}

// pipeline holds the three models and the buffers between them.
type pipeline struct {
	mel        *model
	embedding  *model
	classifier *model

	audio     []float32 // recent raw audio, for mel left-context
	melFrames [][]float32
	features  [][]float32
}

func newPipeline(wakeword string) (*pipeline, error) {
	if err := initRuntime(); err != nil {
		return nil, err
	}
	root := paths.ModelsDir()

	melPath, err := findModelFile(root, "melspectrogram")
	if err != nil {
		return nil, &Error{Reason: err.Error()}
	}
	embeddingPath, err := findModelFile(root, "embedding_model")
	if err != nil {
		return nil, &Error{Reason: err.Error()}
	}
	classifierPath, err := findModelFile(root, wakeword)
	if err != nil {
		return nil, &Error{Reason: fmt.Sprintf(
			"the wake word model %q was not found in %s", wakeword, root)}
	}

	built := &pipeline{}
	if built.mel, err = loadModel(melPath); err != nil {
		return nil, err
	}
	if built.embedding, err = loadModel(embeddingPath); err != nil {
		built.close()
		return nil, err
	}
	if built.classifier, err = loadModel(classifierPath); err != nil {
		built.close()
		return nil, err
	}
	return built, nil
}

func (p *pipeline) close() {
	for _, m := range []*model{p.mel, p.embedding, p.classifier} {
		m.close()
	}
}

// reset clears the buffers, so resuming after a command does not score against
// the command she just spoke.
func (p *pipeline) reset() {
	p.audio = p.audio[:0]
	p.melFrames = p.melFrames[:0]
	p.features = p.features[:0]
}

// score pushes one 80 ms chunk through and returns the current confidence.
//
// It returns 0 until the buffers have filled, which takes about 1.3 seconds
// from a reset. Scoring on a half-full window is what produces false triggers.
func (p *pipeline) score(chunk []float32) (float64, error) {
	p.audio = append(p.audio, chunk...)
	if keep := ChunkSamples + melContext; len(p.audio) > keep {
		p.audio = append(p.audio[:0], p.audio[len(p.audio)-keep:]...)
	}
	if len(p.audio) < ChunkSamples {
		return 0, nil
	}

	newFrames, err := p.melFramesFor(p.audio)
	if err != nil {
		return 0, err
	}
	p.melFrames = append(p.melFrames, newFrames...)
	if keep := melWindow + melFramesPerChunk; len(p.melFrames) > keep {
		p.melFrames = p.melFrames[len(p.melFrames)-keep:]
	}
	if len(p.melFrames) < melWindow {
		return 0, nil
	}

	feature, err := p.embed(p.melFrames[len(p.melFrames)-melWindow:])
	if err != nil {
		return 0, err
	}
	p.features = append(p.features, feature)
	if len(p.features) > featureWindow {
		p.features = p.features[len(p.features)-featureWindow:]
	}
	if len(p.features) < featureWindow {
		return 0, nil
	}
	return p.classify(p.features)
}

// melFramesFor computes the spectrogram over the buffered audio and keeps only
// the frames belonging to the newest chunk. The left context is there so those
// frames are computed with history rather than against a hard edge.
func (p *pipeline) melFramesFor(audio []float32) ([][]float32, error) {
	raw, err := p.mel.run(ort.NewShape(1, int64(len(audio))), audio)
	if err != nil {
		return nil, err
	}
	if len(raw)%melBins != 0 {
		return nil, fmt.Errorf("the spectrogram returned %d values, not a multiple of %d",
			len(raw), melBins)
	}

	total := len(raw) / melBins
	wanted := min(melFramesPerChunk, total)

	frames := make([][]float32, 0, wanted)
	for index := total - wanted; index < total; index++ {
		frame := make([]float32, melBins)
		for bin := 0; bin < melBins; bin++ {
			// openWakeWord's models were trained on this scaling, so it is part
			// of the model contract rather than a tuning knob.
			frame[bin] = raw[index*melBins+bin]/10.0 + 2.0
		}
		frames = append(frames, frame)
	}
	return frames, nil
}

func (p *pipeline) embed(window [][]float32) ([]float32, error) {
	flat := make([]float32, 0, melWindow*melBins)
	for _, frame := range window {
		flat = append(flat, frame...)
	}

	raw, err := p.embedding.run(ort.NewShape(1, melWindow, melBins, 1), flat)
	if err != nil {
		return nil, err
	}
	if len(raw) < embeddingSize {
		return nil, fmt.Errorf("the embedding model returned %d values, want %d",
			len(raw), embeddingSize)
	}
	return raw[len(raw)-embeddingSize:], nil
}

func (p *pipeline) classify(features [][]float32) (float64, error) {
	flat := make([]float32, 0, featureWindow*embeddingSize)
	for _, feature := range features {
		flat = append(flat, feature...)
	}

	raw, err := p.classifier.run(ort.NewShape(1, featureWindow, embeddingSize), flat)
	if err != nil {
		return 0, err
	}
	if len(raw) == 0 {
		return 0, fmt.Errorf("the wake word model returned nothing")
	}
	return float64(raw[len(raw)-1]), nil
}
