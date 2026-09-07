// Package supertonic is the local voice: Supertone's Supertonic 3, run on this
// machine through ONNX Runtime.
//
// It exists because the online Edge voices have one failure the rest of
// MikkiLens does not tolerate. They need the network, they need the clock to
// be roughly right, and they are somebody else's service to withdraw -- and
// when any of that goes wrong mid-stream what she gets is the flat Windows
// voice, or nothing. A voice that lives in data/models cannot be switched off
// from outside.
//
// What it costs is four hundred megabytes on disk, about 450 MB resident, a
// second and a half to open the sessions, and roughly a fifth of a second of
// processor per second of speech. That is why nothing here loads until
// something actually asks it to speak, why the engine is opened once and kept
// rather than built per utterance, and why switching away from it unloads.
//
// The pipeline is four models in a row:
//
//	text -> duration predictor  -> how long this will take to say
//	text -> text encoder        -> what to say
//	noise + both, N times       -> a latent, denoised step by step
//	latent -> vocoder           -> 44.1 kHz audio
//
// Only the third is expensive, and it is the one with a quality dial on it:
// Steps is how many times the estimator runs, and it is the whole of the
// speed-against-quality trade.
package supertonic

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	ort "github.com/yalue/onnxruntime_go"

	"github.com/exzork/mikkilens/packages/audio/onnx"
	"github.com/exzork/mikkilens/packages/core/paths"
)

// Error is a synthesis failure worth reporting aloud.
type Error struct{ Reason string }

func (e *Error) Error() string { return e.Reason }

func failure(format string, args ...any) error {
	return &Error{Reason: fmt.Sprintf(format, args...)}
}

// Dir is where the models live: data/models/supertonic, laid out the way the
// model repository lays them out, so the downloader is a plain mirror and
// anybody can drop a hand-fetched copy in beside it.
func Dir() string { return filepath.Join(paths.ModelsDir(), "supertonic") }

func onnxDir() string  { return filepath.Join(Dir(), "onnx") }
func voiceDir() string { return filepath.Join(Dir(), "voice_styles") }

// ModelFiles are the four networks and the two tables they need, named here
// because the downloader fetches exactly this list and Installed checks it.
var ModelFiles = []string{
	"duration_predictor.onnx",
	"text_encoder.onnx",
	"vector_estimator.onnx",
	"vocoder.onnx",
	"tts.json",
	"unicode_indexer.json",
}

// PresetVoices are the ten the model ships with. Five of each, named by the
// model rather than by us; a voice with a name of its own would be a promise
// about how it sounds that these do not make.
var PresetVoices = []string{"F1", "F2", "F3", "F4", "F5", "M1", "M2", "M3", "M4", "M5"}

// DefaultVoice is a female voice, because MikkiLens is one.
const DefaultVoice = "F1"

// Installed reports whether everything needed to speak is on the machine.
//
// All of it, not some: four models and no voice style is a voice that loads
// and then has nothing to say in, which she experiences as a voice that has
// stopped working rather than as a download that did not finish.
func Installed() bool {
	for _, name := range ModelFiles {
		if !exists(filepath.Join(onnxDir(), name)) {
			return false
		}
	}
	return len(Voices()) > 0
}

// Voice is one voice as the settings page shows it.
type Voice struct {
	Name   string `json:"name"`
	Gender string `json:"gender"`
}

// Voices lists what is installed, which is not necessarily what shipped.
//
// The directory is read rather than PresetVoices returned, so a voice built
// elsewhere and dropped into data/models/supertonic/voice_styles appears in
// the dropdown with no code change. The file is the voice.
func Voices() []Voice {
	entries, err := os.ReadDir(voiceDir())
	if err != nil {
		return nil
	}
	var found []Voice
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".json")
		found = append(found, Voice{Name: name, Gender: genderOf(name)})
	}
	sort.Slice(found, func(a, b int) bool { return found[a].Name < found[b].Name })
	return found
}

// genderOf reads the preset naming convention and admits when it does not
// apply. A voice somebody built themselves is called whatever they called it.
func genderOf(name string) string {
	if len(name) == 2 && name[1] >= '1' && name[1] <= '9' {
		switch name[0] {
		case 'F':
			return "Female"
		case 'M':
			return "Male"
		}
	}
	return ""
}

// Known reports whether a voice name is one this machine can actually use.
func Known(name string) bool {
	for _, voice := range Voices() {
		if voice.Name == name {
			return true
		}
	}
	return false
}

// Options are the knobs on one piece of synthesis.
type Options struct {
	// Voice is a file in voice_styles, without the extension. Empty means
	// DefaultVoice.
	Voice string

	// Language is the tag wrapped around the text. Empty means "na", which
	// reads the characters without committing to a language.
	Language string

	// Steps is how many times the estimator runs. Eight is the model's own
	// default and the point where more stops being audible; five is the floor
	// worth using, and below that it slurs.
	Steps int

	// Speed divides the predicted duration, so a larger number is faster.
	// 1.05 is the model's default and sounds like ordinary speech. Anything
	// outside MinSpeed..MaxSpeed is clamped -- see MaxSpeed for why that is a
	// refusal rather than a preference.
	Speed float32
}

const (
	defaultSteps = 8

	// DefaultSpeed is what the model's authors found sounds like ordinary
	// speech. Not 1.0: a rate of "no change" has to land here, not there.
	DefaultSpeed = 1.05

	// MinSpeed and MaxSpeed are what the model can actually say, rather than
	// what it will accept without complaining.
	//
	// Asking for speech shorter than it can fit does not produce fast speech.
	// The duration predictor's answer is divided by this and the latent is
	// sized from the result, so too large a number means too few columns to
	// hold the words -- and what comes out is the sentence with syllables and
	// then whole words missing from it, at confident volume, sounding finished.
	// Nothing reports this. It is only audible, which on a machine used by ear
	// is the worst way for it to be true.
	//
	// Measured rather than taken from the documentation, by synthesizing a
	// sentence and reading it back with the recognizer this application already
	// ships. Four generations at each speed, because the latent starts from
	// noise and one lucky sample says nothing:
	//
	//	1.30   4/4 word for word, on both sentences tried
	//	1.40   3/4 -- one dropped "atas"
	//	1.50   0/4 -- "Terima katas dukungannya", "kasih banyak-banyak"
	//	1.55   loses the last word outright
	//	2.00   four words of eight
	//
	// So 1.3 rather than the 1.5 the model's own documentation recommends:
	// 1.5 was clean the first time it was tried and wrong every time after,
	// which is the shape of a limit set from one sample. The low end holds
	// to 0.7, which is where that documentation stops.
	//
	// Clamping rather than scaling the whole range onto this one. A rate she
	// set means the same thing whichever voice is reading -- that is the point
	// of it being one setting -- so the top of the slider doing less here is
	// honest, and quietly reinterpreting "+80%" as something else would not be.
	MinSpeed = 0.7
	MaxSpeed = 1.3

	// betweenChunks is the pause spliced between the pieces of a long read.
	// Without it the chunks butt together and the join is audible as a word
	// that starts before the last one has finished.
	betweenChunks = 0.3
)

func (o Options) steps() int {
	if o.Steps <= 0 {
		return defaultSteps
	}
	// More than about twelve buys nothing and costs proportionally, on a
	// machine that is also encoding video.
	return min(o.Steps, 12)
}

func (o Options) speed() float32 {
	if o.Speed <= 0 {
		return DefaultSpeed
	}
	return min(max(o.Speed, MinSpeed), MaxSpeed)
}

func (o Options) voice() string {
	if o.Voice == "" {
		return DefaultVoice
	}
	return o.Voice
}

// settings is the part of tts.json this code actually reads. The file
// describes the whole architecture; four numbers of it decide tensor shapes.
type settings struct {
	AE struct {
		SampleRate    int `json:"sample_rate"`
		BaseChunkSize int `json:"base_chunk_size"`
	} `json:"ae"`
	TTL struct {
		LatentDim           int `json:"latent_dim"`
		ChunkCompressFactor int `json:"chunk_compress_factor"`
	} `json:"ttl"`
}

// Engine holds the four loaded models. Opening one is expensive and holding
// one is expensive; there is normally exactly one, from Shared.
type Engine struct {
	// mu serializes inference. The sessions are not safe to run concurrently,
	// and there is nothing to gain by trying: the speech bus says one thing at
	// a time anyway.
	mu sync.Mutex

	duration  *ort.DynamicAdvancedSession
	encoder   *ort.DynamicAdvancedSession
	estimator *ort.DynamicAdvancedSession
	vocoder   *ort.DynamicAdvancedSession

	indexer []int64

	sampleRate int
	latentDim  int // rows of the latent: latent_dim * chunk_compress_factor
	chunkSize  int // audio samples one latent column covers

	styles map[string]*style
}

// style is one voice, as the two tensors the models want it as. Loaded once
// and kept: the JSON is three hundred kilobytes and parsing it per utterance
// was measurable next to the synthesis itself.
type style struct {
	ttl *ort.Tensor[float32]
	dp  *ort.Tensor[float32]
}

func (s *style) destroy() {
	if s.ttl != nil {
		s.ttl.Destroy()
	}
	if s.dp != nil {
		s.dp.Destroy()
	}
}

// Open loads the four models. It is slow -- most of a second, mostly the
// estimator -- so callers want Shared rather than this.
func Open() (*Engine, error) {
	if err := onnx.Start(); err != nil {
		return nil, &Error{Reason: err.Error() + "; the local voice cannot run without it"}
	}
	if !Installed() {
		return nil, &Error{Reason: "the local voice is not installed; " +
			"its models go in " + Dir()}
	}

	raw, err := os.ReadFile(filepath.Join(onnxDir(), "tts.json"))
	if err != nil {
		return nil, failure("could not read the voice settings: %v", err)
	}
	var config settings
	if err := json.Unmarshal(raw, &config); err != nil {
		return nil, failure("could not read the voice settings: %v", err)
	}
	if config.AE.SampleRate == 0 || config.TTL.LatentDim == 0 ||
		config.TTL.ChunkCompressFactor == 0 || config.AE.BaseChunkSize == 0 {
		return nil, &Error{Reason: "the voice settings are missing the tensor shapes"}
	}

	indexer, err := readIndexer(filepath.Join(onnxDir(), "unicode_indexer.json"))
	if err != nil {
		return nil, err
	}

	// Two threads rather than the core count. The estimator is real work and a
	// second core roughly halves the wait, but this runs on a machine that is
	// encoding video, and taking every core to shave a tenth of a second off a
	// confirmation is the wrong trade. See onnx.Options.
	options, err := onnx.Options(2)
	if err != nil {
		return nil, &Error{Reason: err.Error()}
	}
	defer options.Destroy()

	engine := &Engine{
		indexer:    indexer,
		sampleRate: config.AE.SampleRate,
		latentDim:  config.TTL.LatentDim * config.TTL.ChunkCompressFactor,
		chunkSize:  config.AE.BaseChunkSize * config.TTL.ChunkCompressFactor,
		styles:     map[string]*style{},
	}

	load := func(file string, inputs, outputs []string) (*ort.DynamicAdvancedSession, error) {
		session, err := ort.NewDynamicAdvancedSession(
			filepath.Join(onnxDir(), file), inputs, outputs, options)
		if err != nil {
			return nil, failure("could not load %s: %v", file, err)
		}
		return session, nil
	}

	// The input and output names are hard-coded rather than read from the
	// files, unlike the wake word's. These four are one released set that
	// moves together; a name that changed would mean the pipeline had changed
	// shape too, and failing to load is a better way to find that out than
	// wiring the wrong tensor into the right slot.
	if engine.duration, err = load("duration_predictor.onnx",
		[]string{"text_ids", "style_dp", "text_mask"}, []string{"duration"}); err != nil {
		return nil, err
	}
	if engine.encoder, err = load("text_encoder.onnx",
		[]string{"text_ids", "style_ttl", "text_mask"}, []string{"text_emb"}); err != nil {
		engine.Close()
		return nil, err
	}
	if engine.estimator, err = load("vector_estimator.onnx",
		[]string{"noisy_latent", "text_emb", "style_ttl", "latent_mask", "text_mask",
			"current_step", "total_step"}, []string{"denoised_latent"}); err != nil {
		engine.Close()
		return nil, err
	}
	if engine.vocoder, err = load("vocoder.onnx",
		[]string{"latent"}, []string{"wav_tts"}); err != nil {
		engine.Close()
		return nil, err
	}
	return engine, nil
}

// Close releases the models and the loaded voices.
func (e *Engine) Close() {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()

	for _, session := range []*ort.DynamicAdvancedSession{
		e.duration, e.encoder, e.estimator, e.vocoder,
	} {
		if session != nil {
			_ = session.Destroy()
		}
	}
	e.duration, e.encoder, e.estimator, e.vocoder = nil, nil, nil, nil

	for _, loaded := range e.styles {
		loaded.destroy()
	}
	e.styles = nil
}

// SampleRate is what the vocoder produces: 44.1 kHz, mono.
func (e *Engine) SampleRate() int { return e.sampleRate }

// Speak renders text to mono float32 samples at SampleRate.
//
// Long text is split and the pieces are joined with a short pause, so a read
// aloud donation starts being heard while the rest of it is still being made.
func (e *Engine) Speak(ctx context.Context, text string, options Options) ([]float32, error) {
	pieces := chunk(text, chunkLimit(options.Language))
	if len(pieces) == 0 {
		return nil, &Error{Reason: "there was nothing to say"}
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.estimator == nil {
		return nil, &Error{Reason: "the local voice is closed"}
	}

	loaded, err := e.style(options.voice())
	if err != nil {
		return nil, err
	}

	var samples []float32
	for index, piece := range pieces {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		spoken, err := e.render(ctx, piece, loaded, options)
		if err != nil {
			return nil, err
		}
		if index > 0 {
			samples = append(samples, make([]float32, int(betweenChunks*float64(e.sampleRate)))...)
		}
		samples = append(samples, spoken...)
	}
	return samples, nil
}

// render is one chunk, all the way through.
func (e *Engine) render(ctx context.Context, text string, voice *style, options Options) ([]float32, error) {
	ids := e.index(prepare(text, options.Language))
	if len(ids) == 0 {
		return nil, &Error{Reason: "there was nothing to say"}
	}

	// Everything here is one utterance at a time, so both masks are entirely
	// ones: the mask exists to pad a batch out to its longest member, and a
	// batch of one has nothing to pad. They are still passed because the
	// models take them, and building them is a slice of ones.
	textIDs, err := ort.NewTensor(ort.NewShape(1, int64(len(ids))), ids)
	if err != nil {
		return nil, failure("could not prepare the text: %v", err)
	}
	defer textIDs.Destroy()

	textMask, err := ones(1, 1, int64(len(ids)))
	if err != nil {
		return nil, err
	}
	defer textMask.Destroy()

	// How long this will take to say, which is what sizes everything after it.
	duration, err := run(e.duration, textIDs, voice.dp, textMask)
	if err != nil {
		return nil, failure("could not work out the length: %v", err)
	}
	defer duration.Destroy()

	seconds := duration.GetData()[0] / options.speed()
	if seconds <= 0 || math.IsNaN(float64(seconds)) {
		return nil, &Error{Reason: "the voice predicted a length that makes no sense"}
	}

	embedding, err := run(e.encoder, textIDs, voice.ttl, textMask)
	if err != nil {
		return nil, failure("could not read the text: %v", err)
	}
	defer embedding.Destroy()

	// The latent is the sound as the model thinks about it: latentDim rows,
	// one column per chunkSize audio samples.
	wanted := int(float64(seconds) * float64(e.sampleRate))
	columns := (wanted + e.chunkSize - 1) / e.chunkSize
	if columns < 1 {
		columns = 1
	}

	latentShape := ort.NewShape(1, int64(e.latentDim), int64(columns))
	noise := make([]float32, e.latentDim*columns)
	for index := range noise {
		noise[index] = float32(rand.NormFloat64())
	}

	// The tensor is backed by the noise slice itself, so each step's result is
	// written back into it and the next step reads it without another
	// allocation. Over eight steps of a latent this size that is the
	// difference between a few megabytes of garbage per phrase and none.
	latent, err := ort.NewTensor(latentShape, noise)
	if err != nil {
		return nil, failure("could not prepare the voice: %v", err)
	}
	defer latent.Destroy()

	latentMask, err := ones(1, 1, int64(columns))
	if err != nil {
		return nil, err
	}
	defer latentMask.Destroy()

	steps := options.steps()
	total, err := ort.NewTensor(ort.NewShape(1), []float32{float32(steps)})
	if err != nil {
		return nil, failure("could not prepare the voice: %v", err)
	}
	defer total.Destroy()

	current, err := ort.NewTensor(ort.NewShape(1), []float32{0})
	if err != nil {
		return nil, failure("could not prepare the voice: %v", err)
	}
	defer current.Destroy()

	for step := 0; step < steps; step++ {
		// Checked every step rather than every chunk. This loop is where
		// almost all the time goes, and an utterance that has been cut off --
		// an error preempting a chat message -- should stop being made, not
		// finish being made and then be thrown away.
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		current.GetData()[0] = float32(step)

		denoised, err := run(e.estimator, latent, embedding, voice.ttl,
			latentMask, textMask, current, total)
		if err != nil {
			return nil, failure("could not shape the voice: %v", err)
		}
		copy(noise, denoised.GetData())
		denoised.Destroy()
	}

	// The vocoder reads the same tensor: after the loop it holds the finished
	// latent rather than the noise it started as.
	wave, err := run(e.vocoder, latent)
	if err != nil {
		return nil, failure("could not make the sound: %v", err)
	}
	defer wave.Destroy()

	// The vocoder rounds up to whole chunks, so the tail is padding rather
	// than speech. The predicted duration is where the words actually end.
	produced := wave.GetData()
	if wanted > len(produced) {
		wanted = len(produced)
	}
	return append([]float32(nil), produced[:wanted]...), nil
}

// run feeds inputs through a session and returns the single output tensor,
// which the caller owns and must destroy.
func run(session *ort.DynamicAdvancedSession, inputs ...ort.Value) (*ort.Tensor[float32], error) {
	outputs := []ort.Value{nil}
	if err := session.Run(inputs, outputs); err != nil {
		return nil, err
	}
	tensor, ok := outputs[0].(*ort.Tensor[float32])
	if !ok {
		if outputs[0] != nil {
			_ = outputs[0].Destroy()
		}
		return nil, fmt.Errorf("the model returned %T rather than float32", outputs[0])
	}
	return tensor, nil
}

func ones(dimensions ...int64) (*ort.Tensor[float32], error) {
	shape := ort.NewShape(dimensions...)
	data := make([]float32, shape.FlattenedSize())
	for index := range data {
		data[index] = 1
	}
	tensor, err := ort.NewTensor(shape, data)
	if err != nil {
		return nil, failure("could not prepare the voice: %v", err)
	}
	return tensor, nil
}

// index turns prepared text into the numbers the encoder reads.
//
// A character the table does not have becomes -1, which the models read as
// their last embedding -- the slot they were trained to use for exactly this.
// Most of Unicode is -1; it is the ordinary case, not an error.
func (e *Engine) index(text string) []int64 {
	runes := []rune(text)
	ids := make([]int64, len(runes))
	for at, letter := range runes {
		if int(letter) < len(e.indexer) {
			ids[at] = e.indexer[letter]
		} else {
			ids[at] = -1
		}
	}
	return ids
}

// style loads one voice, or returns the one already loaded.
func (e *Engine) style(name string) (*style, error) {
	if loaded, ok := e.styles[name]; ok {
		return loaded, nil
	}

	path := filepath.Join(voiceDir(), name+".json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, failure("the voice %q is not installed", name)
	}

	var parsed struct {
		TTL styleTensor `json:"style_ttl"`
		DP  styleTensor `json:"style_dp"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, failure("could not read the voice %q: %v", name, err)
	}

	ttl, err := parsed.TTL.tensor()
	if err != nil {
		return nil, failure("could not read the voice %q: %v", name, err)
	}
	dp, err := parsed.DP.tensor()
	if err != nil {
		ttl.Destroy()
		return nil, failure("could not read the voice %q: %v", name, err)
	}

	loaded := &style{ttl: ttl, dp: dp}
	e.styles[name] = loaded
	return loaded, nil
}

// styleTensor is one of the two arrays in a voice file: nested data and the
// shape it is meant to be.
type styleTensor struct {
	Data [][][]float64 `json:"data"`
	Dims []int64       `json:"dims"`
}

func (s styleTensor) tensor() (*ort.Tensor[float32], error) {
	if len(s.Dims) != 3 {
		return nil, fmt.Errorf("expected three dimensions, got %d", len(s.Dims))
	}
	shape := ort.NewShape(s.Dims...)
	flat := make([]float32, 0, shape.FlattenedSize())
	for _, batch := range s.Data {
		for _, row := range batch {
			for _, value := range row {
				flat = append(flat, float32(value))
			}
		}
	}
	if int64(len(flat)) != shape.FlattenedSize() {
		return nil, fmt.Errorf("the shape says %d values and the file has %d",
			shape.FlattenedSize(), len(flat))
	}
	return ort.NewTensor(shape, flat)
}

// readIndexer loads the character table: one entry per code point in the basic
// plane, so a lookup is an array index rather than a map probe.
func readIndexer(path string) ([]int64, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, failure("could not read the character table: %v", err)
	}
	var table []int64
	if err := json.Unmarshal(raw, &table); err != nil {
		return nil, failure("could not read the character table: %v", err)
	}
	if len(table) == 0 {
		return nil, &Error{Reason: "the character table is empty"}
	}
	return table, nil
}

func exists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Size() > 0
}
