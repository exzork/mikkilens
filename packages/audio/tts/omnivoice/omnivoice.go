// Package omnivoice is the second voice that runs on this machine: k2-fsa's
// OmniVoice, through ONNX Runtime.
//
// It is not a bigger Supertonic. It is a different kind of thing, and the
// difference is worth knowing before choosing it.
//
// Supertonic is a pipeline: text goes in one end, four networks run once each,
// and audio comes out. OmniVoice is a language model that happens to speak. It
// has no fixed voices -- it takes a voice from a recording of one -- and it
// does not read text so much as fill in a gap. The audio it is going to make
// starts as a block of nothing, C codebooks by T frames of it, and every one
// of those slots holds the token id 1024, which means "not decided yet".
// Thirty-two times over, the model looks at the whole thing -- the tags, the
// text, the reference voice, and the part of the audio already decided -- and
// says what it thinks every undecided slot should be. The most confident
// handful are written down and stop being undecided. When nothing is undecided
// the codes go to a decoder, which turns them into 24 kHz sound.
//
// Three consequences follow from that, and all three are felt:
//
//	The length is chosen up front. The model fills exactly the number of
//	frames it is given, so getting that number wrong does not produce
//	slightly-wrong timing, it produces a sentence cut off mid-word or a
//	sentence followed by invented breathing. See duration.go.
//
//	It wants a graphics card. Each step is a full forward pass over the
//	whole sequence -- there is nothing to cache, because every position can
//	change on every pass -- so one sentence is dozens of forward passes.
//	Measured here: about 25x slower than real time on a processor, and
//	faster than real time on an NVIDIA card. That is not a tuning
//	difference, it is the difference between a voice that can be streamed
//	with and one that cannot, which is why Open asks for the card.
//
//	It sounds like whoever it was given. There is no "F1" here. A voice is
//	a wav file somebody recorded, and the model's own quality depends far
//	more on that recording than on anything in this file.
//
// What it costs: about two gigabytes on disk for the models, about another
// gigabyte for the graphics runtime, and about 1.4 GB resident once the
// language model and the decoder are open. Nothing here loads until something
// asks it to speak, and switching away unloads -- the same rules Supertonic
// plays by, for the same reason, only more so.
package omnivoice

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

// The shape of the model, which is not configurable: these are what the
// exported graphs were built around and a different value is a different set
// of files, not a setting.
const (
	// Codebooks is how many tokens describe one frame of audio. The audio
	// tokenizer is residual: the first codebook is a coarse guess and each of
	// the other seven corrects what is left, so all eight are needed and the
	// first matters most. That ordering is why decoding prefers to settle the
	// low ones first -- see layerPenalty.
	Codebooks = 8

	// AudioVocab is 1024 real codes plus one more, MaskID, which is not a
	// sound but the absence of a decision.
	AudioVocab = 1025
	MaskID     = 1024

	// FrameRate is how many audio frames make a second, and SampleRate is what
	// the decoder produces. 24 kHz at 25 frames a second is 960 samples per
	// frame; openTest checks that the decoder agrees.
	FrameRate  = 25
	SampleRate = 24000
)

// The tags the prompt is assembled from. They are real tokens in the
// vocabulary rather than text the model reads, which is why they are looked up
// by name at load time and a missing one is a refusal to load.
const (
	tagDenoise       = "<|denoise|>"
	tagLanguageStart = "<|lang_start|>"
	tagLanguageEnd   = "<|lang_end|>"
	tagInstructStart = "<|instruct_start|>"
	tagInstructEnd   = "<|instruct_end|>"
	tagTextStart     = "<|text_start|>"
	tagTextEnd       = "<|text_end|>"
)

// Error is a synthesis failure worth reporting aloud.
type Error struct{ Reason string }

func (e *Error) Error() string { return e.Reason }

func failure(format string, args ...any) error {
	return &Error{Reason: fmt.Sprintf(format, args...)}
}

// Dir is where the models live: data/models/omnivoice, laid out the way the
// export lays them out, so the downloader is a plain mirror and a hand-fetched
// copy can be dropped in beside it.
func Dir() string { return filepath.Join(paths.ModelsDir(), "omnivoice") }

func onnxDir() string       { return filepath.Join(Dir(), "onnx") }
func voiceDir() string      { return filepath.Join(Dir(), "voices") }
func tokenizerPath() string { return filepath.Join(Dir(), "tokenizer.json") }

// ModelFiles are what has to be present to speak at all.
//
// Each graph is a pair: a small .onnx holding the structure and a large .data
// holding the weights, which ONNX Runtime finds by looking beside the graph.
// Both halves are named here because half a pair is a model that loads and
// then fails on the first tensor, which is a much worse way to discover an
// unfinished download than being told it is unfinished.
var ModelFiles = []string{
	"omnivoice_lm.onnx",
	"omnivoice_lm.onnx.data",
	"audio_decoder.onnx",
	"audio_decoder.onnx.data",
}

// EncoderFiles are needed only to take a voice from a recording, which happens
// once per recording and never again. They are listed apart from ModelFiles so
// somebody who only ever uses prepared voices is not told their installation
// is broken. See Prepare.
var EncoderFiles = []string{
	"audio_encoder.onnx",
	"audio_encoder.onnx.data",
}

// Installed reports whether everything needed to speak is on the machine.
func Installed() bool {
	for _, name := range ModelFiles {
		if !exists(filepath.Join(onnxDir(), name)) {
			return false
		}
	}
	return exists(tokenizerPath())
}

// EncoderInstalled reports whether a new recording could be turned into a
// voice right now.
func EncoderInstalled() bool {
	for _, name := range EncoderFiles {
		if !exists(filepath.Join(onnxDir(), name)) {
			return false
		}
	}
	return true
}

func exists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// Options are the knobs on one piece of synthesis.
type Options struct {
	// Voice is the name of a prepared voice in data/models/omnivoice/voices.
	// Empty means no reference at all, which the model answers in a voice of
	// its own invention -- different every time, which is why it is not a
	// default anybody should be left on by accident.
	Voice string

	// Language is a hint the model is given as a tag, not a setting that
	// changes which weights run. Empty becomes "None", which is what the
	// model was trained to see when nobody said.
	Language string

	// Instruct describes a voice in words -- "female, low pitch, british
	// accent" -- for when there is no recording to take one from. It is
	// ignored when Voice names a prepared voice, because a recording says
	// everything a description was approximating.
	Instruct string

	// Speed multiplies the predicted length, the opposite way round: 1.25
	// means a quarter faster, so a quarter fewer frames to fill. The model
	// has no speed input of its own; this is the only lever, and it works
	// because the model fits the words into however long it is given.
	Speed float32

	// Steps is how many times the whole sequence is looked at. It is the
	// speed-against-quality dial and the only one that matters: the work is
	// exactly proportional to it. Zero means DefaultSteps.
	Steps int

	// Guidance is classifier-free guidance: how hard to push the text-
	// conditioned answer away from the unconditioned one. Zero means
	// DefaultGuidance; a real zero is not available, and would not be wanted.
	Guidance float32
}

// The decoding defaults.
//
// Steps departs from the model's own default of 32, which is the number its
// authors benchmark at on a datacentre graphics card. Here every step is
// seconds of processor, and sixteen is the number their own documentation
// offers for faster inference. Doubling it roughly doubles the wait for an
// improvement that does not survive a live stream's own audio chain.
const (
	DefaultSteps    = 16
	DefaultGuidance = 2.0

	// timeShift bends the schedule so the early steps commit to fewer tokens
	// than an even split would. The early decisions are the ones every later
	// decision is conditioned on, so they are worth making slowly.
	timeShift = 0.1

	// layerPenalty pushes the eight codebooks to settle roughly in order. The
	// first codebook is the coarse shape of the sound and the rest are
	// corrections to it; deciding a correction before the thing it corrects
	// is deciding it against nothing.
	layerPenalty = 5.0

	// positionTemperature is noise on the choice of *which* slots to settle,
	// not on what to put in them. Without it, decoding takes the most
	// confident slots every time, which in practice means it fills the
	// silences first and then has to fit the words into what is left.
	positionTemperature = 5.0

	// classTemperature is noise on *what* goes in a slot. Zero -- take the
	// most likely code -- is the model's own default and what makes repeated
	// renders of the same sentence in the same voice come out the same, which
	// is what the speech cache upstream assumes.
	classTemperature = 0.0
)

func (o Options) steps() int {
	if o.Steps <= 0 {
		return DefaultSteps
	}
	return o.Steps
}

func (o Options) guidance() float32 {
	if o.Guidance == 0 {
		return DefaultGuidance
	}
	return o.Guidance
}

func (o Options) speed() float32 {
	if o.Speed <= 0 {
		return 1
	}
	return o.Speed
}

func (o Options) language() string {
	if strings.TrimSpace(o.Language) == "" {
		return "None"
	}
	return o.Language
}

func (o Options) instruct() string {
	if strings.TrimSpace(o.Instruct) == "" {
		return "None"
	}
	return o.Instruct
}

// Engine holds the two loaded graphs and the tokenizer. Opening one is
// expensive and holding one is expensive; there is normally exactly one, from
// Shared.
type Engine struct {
	// mu serializes inference. The sessions are not safe to run concurrently
	// and there is nothing to gain by trying: the speech bus says one thing at
	// a time anyway.
	mu sync.Mutex

	lm      *ort.DynamicAdvancedSession
	decoder *ort.DynamicAdvancedSession

	tokenizer *Tokenizer
	tags      map[string]int32

	voices map[string]*Voice

	// accelerated is whether the graphics card took the work. It is kept so
	// the status page can say which of the two very different speeds she is
	// about to experience, rather than leaving it to be discovered as a pause.
	accelerated bool
}

// Accelerated reports whether the models are running on the graphics card.
func (e *Engine) Accelerated() bool {
	if e == nil {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.accelerated
}

// Open loads the language model and the decoder.
//
// It is slow and it is 1.4 GB, so callers want Shared rather than this.
func Open() (*Engine, error) {
	if err := onnx.Start(); err != nil {
		return nil, &Error{Reason: err.Error() + "; OmniVoice cannot run without it"}
	}
	if !Installed() {
		return nil, &Error{Reason: "OmniVoice is not installed; its models go in " + Dir()}
	}

	tokenizer, err := LoadTokenizer(tokenizerPath())
	if err != nil {
		return nil, err
	}

	engine := &Engine{
		tokenizer: tokenizer,
		tags:      map[string]int32{},
		voices:    map[string]*Voice{},
	}
	for _, tag := range []string{
		tagDenoise, tagLanguageStart, tagLanguageEnd,
		tagInstructStart, tagInstructEnd, tagTextStart, tagTextEnd,
	} {
		id, err := tokenizer.Special(tag)
		if err != nil {
			return nil, err
		}
		engine.tags[tag] = id
	}

	// The graphics card if there is one, and the processor if not.
	//
	// This is the only model in MikkiLens where that choice decides whether
	// the feature works at all rather than how quickly. On a processor this is
	// roughly twenty-five seconds of work per second of speech -- a sentence
	// takes half a minute -- and on a card it is faster than real time. See
	// onnx.Accelerated.
	//
	// Four threads when it falls back to the processor, rather than
	// Supertonic's two. Supertonic's second core buys a tenth of a second;
	// here the choice is between taking cores and the sentence arriving after
	// the moment it was about. Four still leaves most of a modern machine to
	// the game and the encoder.
	options, accelerated, err := onnx.Accelerated(4, 0)
	if err != nil {
		return nil, &Error{Reason: err.Error()}
	}
	defer options.Destroy()
	engine.accelerated = accelerated

	if engine.lm, err = load(options, "omnivoice_lm.onnx",
		[]string{"input_ids", "audio_mask", "attention_mask"},
		[]string{"logits"}); err != nil {
		return nil, err
	}

	// The decoder stays on the processor even when the card took the language
	// model, and that is the whole point rather than an oversight.
	//
	// It runs once per sentence against the language model's thirty-two passes,
	// so it is a rounding error in the total either way. What it is not a
	// rounding error in is the download: it is the only graph here with
	// convolutions in it, convolutions on the card need cuDNN, and cuDNN is
	// about a gigabyte of libraries that nothing else in MikkiLens would ever
	// load. Paying a fraction of a second per sentence to not ship that is an
	// easy trade on a machine that is also streaming.
	processor, err := onnx.Options(2)
	if err != nil {
		engine.Close()
		return nil, &Error{Reason: err.Error()}
	}
	defer processor.Destroy()

	if engine.decoder, err = load(processor, "audio_decoder.onnx",
		[]string{"audio_codes"}, []string{"audio_values"}); err != nil {
		engine.Close()
		return nil, err
	}
	return engine, nil
}

func load(options *ort.SessionOptions, file string,
	inputs, outputs []string) (*ort.DynamicAdvancedSession, error) {

	session, err := ort.NewDynamicAdvancedSession(
		filepath.Join(onnxDir(), file), inputs, outputs, options)
	if err != nil {
		return nil, failure("could not load %s: %v", file, err)
	}
	return session, nil
}

// Close releases the models.
func (e *Engine) Close() {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()

	for _, session := range []*ort.DynamicAdvancedSession{e.lm, e.decoder} {
		if session != nil {
			_ = session.Destroy()
		}
	}
	e.lm, e.decoder = nil, nil
	e.voices = nil
}

// SampleRate is what the decoder produces: 24 kHz, mono.
func (e *Engine) SampleRate() int { return SampleRate }

// Speak renders text to mono float32 samples at SampleRate.
//
// Long text is split and the pieces are joined with a short pause. That is not
// only about latency here, as it is for Supertonic: attention costs the square
// of the sequence length, and the sequence includes every frame of audio being
// made, so one long read is meaningfully more expensive than the same words in
// three pieces.
func (e *Engine) Speak(ctx context.Context, text string, options Options) ([]float32, error) {
	pieces := chunk(text)
	if len(pieces) == 0 {
		return nil, &Error{Reason: "there was nothing to say"}
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.lm == nil {
		return nil, &Error{Reason: "OmniVoice is closed"}
	}

	voice, err := e.voice(options.Voice)
	if err != nil {
		return nil, err
	}

	var samples []float32
	for index, piece := range pieces {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		spoken, err := e.render(ctx, piece, voice, options)
		if err != nil {
			return nil, err
		}
		if index > 0 {
			samples = append(samples, make([]float32, int(betweenChunks*SampleRate))...)
		}
		samples = append(samples, spoken...)
	}
	return samples, nil
}

// betweenChunks is the pause spliced between the pieces of a long read.
const betweenChunks = 0.25

// render is one chunk, all the way through: decide the length, build the
// prompt, run the decoding loop, turn codes into sound.
func (e *Engine) render(ctx context.Context, text string,
	voice *Voice, options Options) ([]float32, error) {

	referenceText, referenceFrames := "", 0
	if voice != nil {
		referenceText, referenceFrames = voice.Text, voice.Frames()
	}
	frames := estimateFrames(text, referenceText, referenceFrames, options.speed())

	prompt, err := e.buildPrompt(text, voice, frames, options)
	if err != nil {
		return nil, err
	}

	codes, err := e.decodeTokens(ctx, prompt, frames, options)
	if err != nil {
		return nil, err
	}

	samples, err := e.synthesize(codes, frames)
	if err != nil {
		return nil, err
	}
	return postProcess(samples, voice), nil
}

// prompt is one assembled input to the language model.
//
// The layout is fixed and every part of it is load-bearing:
//
//	[ style tags ][ text ][ reference audio ][ the part being made ]
//	|-------- audioMask false -------||------ audioMask true ------|
//
// The reference audio sits inside the audio region rather than beside it,
// which is the whole trick of zero-shot cloning: the model is not told "use
// this voice", it is shown a stretch of audio that is already decided and asked
// to continue in the same vein.
type prompt struct {
	ids       []int32 // Codebooks rows of length total, row-major
	total     int     // sequence length
	audioFrom int     // first position the audio mask is true for
	targetAt  int     // first position of the part being made
}

func (e *Engine) buildPrompt(text string, voice *Voice,
	frames int, options Options) (*prompt, error) {

	// The style tags say what kind of read this is. <|denoise|> asks the model
	// to clean up what it heard rather than reproduce it faithfully, and is
	// only meaningful when there is a recording to clean up.
	var style []int32
	if voice != nil {
		style = append(style, e.tags[tagDenoise])
	}
	style = append(style, e.tags[tagLanguageStart])
	style = append(style, e.tokenizer.Encode(options.language())...)
	style = append(style, e.tags[tagLanguageEnd])
	style = append(style, e.tags[tagInstructStart])
	style = append(style, e.tokenizer.Encode(e.instructFor(voice, options))...)
	style = append(style, e.tags[tagInstructEnd])

	// The reference transcript is prepended to the text rather than given its
	// own field, because to the model this is one continuous read: the
	// recording is the first half of it and the words being asked for are the
	// second. That is also why a recording with no transcript still works and
	// works less well -- the model hears a voice saying something it was not
	// told about.
	reading := combineText(text, referenceTextOf(voice))
	words := []int32{e.tags[tagTextStart]}
	words = append(words, e.tokenizer.Encode(reading)...)
	words = append(words, e.tags[tagTextEnd])

	referenceFrames := 0
	if voice != nil {
		referenceFrames = voice.Frames()
	}
	total := len(style) + len(words) + referenceFrames + frames
	if total < 1 {
		return nil, &Error{Reason: "there was nothing to say"}
	}

	built := &prompt{
		ids:       make([]int32, Codebooks*total),
		total:     total,
		audioFrom: len(style) + len(words),
		targetAt:  len(style) + len(words) + referenceFrames,
	}

	// The text goes into every one of the eight rows, identically. Only row
	// zero is ever read as text -- the model embeds input_ids[:, 0, :] for the
	// text positions -- but the other seven are multiplied by the audio mask
	// and summed, so leaving them holding something else would add noise to
	// nothing. Copying the text is what makes that sum come out right.
	for row := 0; row < Codebooks; row++ {
		at := row * total
		copy(built.ids[at:], style)
		copy(built.ids[at+len(style):], words)
		if voice != nil {
			copy(built.ids[at+built.audioFrom:], voice.Codes[row])
		}
		for column := built.targetAt; column < total; column++ {
			built.ids[at+column] = MaskID
		}
	}
	return built, nil
}

// instructFor decides what goes between the instruct tags.
//
// A recording wins over a description of one. Giving the model both is giving
// it two answers to the same question, and the one it was mostly trained on is
// the recording.
func (e *Engine) instructFor(voice *Voice, options Options) string {
	if voice != nil {
		return "None"
	}
	return options.instruct()
}

func referenceTextOf(voice *Voice) string {
	if voice == nil {
		return ""
	}
	return voice.Text
}

// -- the decoding loop ---------------------------------------------------------

// decodeTokens runs the model until nothing is undecided.
//
// Two forward passes per step rather than one batched pass of two. The
// batched form is what the model's own code does, and it needs an attention
// mask to keep the shorter of the two from reading the longer one's padding --
// but this export folds that mask into the attention scores by adding it,
// after casting it to a boolean, which means a mask that says "do not look
// here" arrives as a slightly larger score rather than as a refusal. Running
// each sequence at its own exact length removes the padding, which removes the
// need for the mask: every position may look at every other, which is what the
// original asks for anyway, and a mask that is uniformly true adds the same
// constant to every score in a row. Softmax does not notice a constant.
//
// It is also less work. Padding the unconditioned pass out to the conditioned
// pass's length would have it spend most of its time on positions whose only
// purpose is to be ignored.
func (e *Engine) decodeTokens(ctx context.Context, built *prompt,
	frames int, options Options) ([][]int32, error) {

	steps := options.steps()
	guidance := options.guidance()

	// tokens is the part being made, and the only thing that changes between
	// steps. It starts entirely undecided.
	tokens := make([][]int32, Codebooks)
	for row := range tokens {
		tokens[row] = make([]int32, frames)
		for column := range tokens[row] {
			tokens[row][column] = MaskID
		}
	}

	conditioned, err := newPass(built.ids, built.total, built.audioFrom)
	if err != nil {
		return nil, err
	}
	defer conditioned.close()

	// The unconditioned pass sees the audio being made and nothing else: no
	// text, no reference, no tags. What guidance then measures is how much of
	// each decision came from being told what to say. Its audio region starts
	// at zero because all of it is audio.
	unconditioned, err := newPass(make([]int32, Codebooks*frames), frames, 0)
	if err != nil {
		return nil, err
	}
	defer unconditioned.close()

	schedule := unmaskSchedule(steps, Codebooks*frames)
	scores := make([]float64, Codebooks*frames)
	predicted := make([]int32, Codebooks*frames)
	withText := make([]float32, AudioVocab)
	without := make([]float32, AudioVocab)

	for step := 0; step < steps; step++ {
		settling := schedule[step]
		if settling <= 0 {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		// Write what has been decided so far into both prompts. Both tensors
		// are backed by these slices, so this is the whole of "updating the
		// model's input".
		for row := 0; row < Codebooks; row++ {
			copy(conditioned.ids[row*conditioned.length+built.targetAt:], tokens[row])
			copy(unconditioned.ids[row*frames:], tokens[row])
		}

		if err := e.forward(conditioned); err != nil {
			return nil, err
		}
		if err := e.forward(unconditioned); err != nil {
			return nil, err
		}

		for row := 0; row < Codebooks; row++ {
			for column := 0; column < frames; column++ {
				at := row*frames + column
				if tokens[row][column] != MaskID {
					// Already decided. Nothing reopens a decision; that is
					// what makes this terminate.
					scores[at] = math.Inf(-1)
					continue
				}

				// The conditioned pass returns logits for the whole sequence,
				// and only its tail is about the audio being made.
				conditioned.at(row, built.targetAt+column, withText)
				unconditioned.at(row, column, without)

				code, confidence := choose(withText, without, guidance)
				predicted[at] = code

				// Settle the coarse codebooks before the corrections to them,
				// and add noise to the choice of which slots go next so
				// decoding does not simply fill every silence first.
				confidence -= float64(row) * layerPenalty
				if positionTemperature > 0 {
					confidence = confidence/positionTemperature + gumbel()
				}
				scores[at] = confidence
			}
		}

		for _, at := range highest(scores, settling) {
			tokens[at/frames][at%frames] = predicted[at]
		}
	}

	// A slot still undecided here would be the id 1024 going to a decoder that
	// has 1024 codes, which is a crash rather than a bad noise. The schedule
	// makes it impossible -- the last step settles whatever is left -- but the
	// schedule is arithmetic and the decoder is a C library.
	for row := range tokens {
		for column := range tokens[row] {
			if tokens[row][column] == MaskID {
				return nil, &Error{Reason: "OmniVoice left part of the audio undecided"}
			}
		}
	}
	return tokens, nil
}

// unmaskSchedule is how many slots are settled on each step.
//
// The steps are not even. timeShift bends them so the first step settles a
// small number and the last settles whatever is left, because the first
// decisions are the ones every later decision is made against. The last step
// takes the remainder outright rather than its share, so the loop is
// guaranteed to finish with nothing undecided however the rounding fell.
func unmaskSchedule(steps, total int) []int {
	points := make([]float64, steps+1)
	for index := range points {
		at := float64(index) / float64(steps)
		points[index] = timeShift * at / (1 + (timeShift-1)*at)
	}

	schedule := make([]int, steps)
	remaining := total
	for step := 0; step < steps; step++ {
		if step == steps-1 {
			schedule[step] = remaining
			break
		}
		want := int(math.Ceil(float64(total) * (points[step+1] - points[step])))
		if want > remaining {
			want = remaining
		}
		if want < 0 {
			want = 0
		}
		schedule[step] = want
		remaining -= want
	}
	return schedule
}

// choose turns one position's two sets of logits into a code and a confidence.
//
// Classifier-free guidance: the conditioned answer is pushed away from the
// unconditioned one, which sharpens whatever the text and the reference voice
// contributed and leaves whatever the model would have done anyway behind.
// Everything is in log space, and the mask token is ruled out afterwards
// rather than before -- it takes part in the normalisation, it just cannot win.
func choose(conditioned, unconditioned []float32, guidance float32) (int32, float64) {
	combined := chooseScratch
	logSoftmaxInto(conditioned, combined)
	logSoftmaxInto(unconditioned, guidanceScratch)
	for index := range combined {
		combined[index] += float64(guidance) * (combined[index] - guidanceScratch[index])
	}
	normalizeLog(combined)
	combined[MaskID] = math.Inf(-1)

	best, bestAt := math.Inf(-1), 0
	for index, value := range combined {
		if value > best {
			best, bestAt = value, index
		}
	}

	if classTemperature > 0 {
		return int32(sampleTopK(combined)), best
	}
	return int32(bestAt), best
}

// The two buffers choose works in. It is called once per undecided slot per
// step -- tens of thousands of times for one sentence -- and the engine holds
// a lock across the whole utterance, so reusing them is safe and is the
// difference between a few megabytes of garbage per sentence and none.
var (
	chooseScratch   = make([]float64, AudioVocab)
	guidanceScratch = make([]float64, AudioVocab)
)

// sampleTopK picks among the most likely tenth rather than taking the best.
//
// Unused while classTemperature is zero, and kept because the alternative is
// that turning that dial up means writing this under a deadline. See the note
// on classTemperature for why it is zero.
func sampleTopK(logProbs []float64) int {
	keep := int(math.Ceil(0.1 * float64(len(logProbs))))
	if keep < 1 {
		keep = 1
	}
	sorted := append([]float64(nil), logProbs...)
	sort.Float64s(sorted)
	threshold := sorted[len(sorted)-keep]

	best, bestAt := math.Inf(-1), 0
	for index, value := range logProbs {
		if value < threshold {
			continue
		}
		noisy := value/classTemperature + gumbel()
		if noisy > best {
			best, bestAt = noisy, index
		}
	}
	return bestAt
}

// highest returns the indices of the `count` largest scores.
//
// A full sort of a few thousand scores, a few dozen times per sentence, is
// nothing next to a forward pass, and it is easier to be sure of than a
// partial selection.
func highest(scores []float64, count int) []int {
	order := make([]int, 0, len(scores))
	for index, value := range scores {
		if math.IsInf(value, -1) {
			continue // already decided, or ruled out
		}
		order = append(order, index)
	}
	sort.SliceStable(order, func(a, b int) bool {
		return scores[order[a]] > scores[order[b]]
	})
	if count > len(order) {
		count = len(order)
	}
	return order[:count]
}

// gumbel is noise shaped so that adding it to log probabilities and taking the
// largest is the same as drawing from those probabilities.
func gumbel() float64 {
	return -math.Log(-math.Log(rand.Float64()+1e-10) + 1e-10)
}

func logSoftmaxInto(values []float32, out []float64) {
	for index, value := range values {
		out[index] = float64(value)
	}
	normalizeLog(out)
}

// normalizeLog turns scores into log probabilities in place, shifting by the
// largest first so that exp never overflows.
func normalizeLog(values []float64) {
	largest := math.Inf(-1)
	for _, value := range values {
		if value > largest {
			largest = value
		}
	}
	if math.IsInf(largest, 0) {
		return
	}
	total := 0.0
	for _, value := range values {
		total += math.Exp(value - largest)
	}
	offset := largest + math.Log(total)
	for index := range values {
		values[index] -= offset
	}
}

// -- running the graphs --------------------------------------------------------

// pass is everything one sequence length needs to be run repeatedly.
//
// Built once per utterance and reused for every step, because within one
// utterance neither sequence length changes -- only the contents of the audio
// region do. The tensors are backed by Go slices that stay writable, so a step
// is a few thousand int32 writes and a Run rather than four allocations, and
// the six megabytes of logits are written over in place instead of being
// allocated and thrown away sixteen times.
//
// The output tensor is allocated here rather than left to ONNX Runtime to
// allocate, which is not a preference. The Go binding sizes an automatically
// allocated tensor of a type it has no Go equivalent for by its element count
// rather than its byte count, and this graph's logits are half precision --
// two bytes each -- so the buffer comes out half the size it needs to be and
// the run fails outright. Handing it a correctly sized buffer avoids that path
// entirely.
type pass struct {
	length int

	ids        []int32
	idTensor   *ort.Tensor[int32]
	maskTensor *ort.Tensor[float32]
	attnTensor *ort.Tensor[float32]

	raw    []byte
	logits *ort.CustomDataTensor
}

// newPass wires the tensors for a sequence of the given length, whose audio
// region starts at audioFrom.
//
// The attention mask is uniformly one. See the note on decodeTokens: with no
// padding anywhere, every position may look at every other, which is what the
// model asks for, and a constant added to every score in a row is invisible to
// softmax.
func newPass(ids []int32, length, audioFrom int) (*pass, error) {
	built := &pass{length: length, ids: ids}

	var err error
	if built.idTensor, err = ort.NewTensor(
		ort.NewShape(1, Codebooks, int64(length)), ids); err != nil {
		return nil, failure("could not prepare the text: %v", err)
	}

	mask := make([]float32, length)
	for index := audioFrom; index < length; index++ {
		mask[index] = 1
	}
	if built.maskTensor, err = ort.NewTensor(
		ort.NewShape(1, int64(length)), mask); err != nil {
		built.close()
		return nil, failure("could not prepare the voice: %v", err)
	}

	attention := make([]float32, length*length)
	for index := range attention {
		attention[index] = 1
	}
	if built.attnTensor, err = ort.NewTensor(
		ort.NewShape(1, 1, int64(length), int64(length)), attention); err != nil {
		built.close()
		return nil, failure("could not prepare the voice: %v", err)
	}

	built.raw = make([]byte, Codebooks*length*AudioVocab*2)
	if built.logits, err = ort.NewCustomDataTensor(
		ort.NewShape(1, Codebooks, int64(length), AudioVocab),
		built.raw, ort.TensorElementDataTypeFloat16); err != nil {
		built.close()
		return nil, failure("could not prepare the voice: %v", err)
	}
	return built, nil
}

func (p *pass) close() {
	if p == nil {
		return
	}
	// Spelled out one field at a time rather than looped over: these are four
	// different generic instantiations and a non-generic type, and gathering
	// them into a slice of an interface would turn a nil pointer into a
	// non-nil interface holding nil, which is the shape of a nil check that
	// passes and then panics.
	if p.idTensor != nil {
		_ = p.idTensor.Destroy()
		p.idTensor = nil
	}
	if p.maskTensor != nil {
		_ = p.maskTensor.Destroy()
		p.maskTensor = nil
	}
	if p.attnTensor != nil {
		_ = p.attnTensor.Destroy()
		p.attnTensor = nil
	}
	if p.logits != nil {
		_ = p.logits.Destroy()
		p.logits = nil
	}
}

// at widens one position's logits out of half precision into `into`.
//
// Per position rather than per pass: every value here is read exactly once, by
// the softmax in choose, and widening the whole six megabytes up front would
// allocate three times as much memory to do the same arithmetic in a less
// cache-friendly order.
func (p *pass) at(row, column int, into []float32) {
	start := ((row*p.length + column) * AudioVocab) * 2
	for index := 0; index < AudioVocab; index++ {
		bits := uint16(p.raw[start+index*2]) | uint16(p.raw[start+index*2+1])<<8
		into[index] = math.Float32frombits(halfToBits(bits))
	}
}

// forward runs the language model once, over the ids currently in the pass.
func (e *Engine) forward(built *pass) error {
	err := e.lm.Run(
		[]ort.Value{built.idTensor, built.maskTensor, built.attnTensor},
		[]ort.Value{built.logits})
	if err != nil {
		return failure("OmniVoice could not read the text: %v", err)
	}
	return nil
}

// widenHalf turns IEEE-754 half precision into float32.
//
// Written out rather than reached for, because Go's standard library has no
// float16 and this is worth having in one obvious place: the subnormal case is
// the one that is easy to get wrong and never notice, because it only affects
// values near zero.
func halfToBits(half uint16) uint32 {
	sign := uint32(half&0x8000) << 16
	exponent := uint32(half>>10) & 0x1F
	mantissa := uint32(half & 0x03FF)

	switch exponent {
	case 0:
		if mantissa == 0 {
			return sign // a signed zero
		}
		// Subnormal: normalise it by hand into a float32, which has enough
		// exponent range to hold it as an ordinary number.
		exponent = 127 - 15 + 1
		for mantissa&0x0400 == 0 {
			mantissa <<= 1
			exponent--
		}
		mantissa &= 0x03FF
		return sign | exponent<<23 | mantissa<<13
	case 0x1F:
		// Infinity or not-a-number, both of which carry across unchanged.
		return sign | 0xFF<<23 | mantissa<<13
	default:
		return sign | (exponent+127-15)<<23 | mantissa<<13
	}
}

// synthesize turns settled codes into sound.
func (e *Engine) synthesize(codes [][]int32, frames int) ([]float32, error) {
	flat := make([]int32, Codebooks*frames)
	for row := 0; row < Codebooks; row++ {
		copy(flat[row*frames:], codes[row])
	}

	tensor, err := ort.NewTensor(ort.NewShape(1, Codebooks, int64(frames)), flat)
	if err != nil {
		return nil, failure("could not prepare the audio: %v", err)
	}
	defer tensor.Destroy()

	outputs := []ort.Value{nil}
	if err := e.decoder.Run([]ort.Value{tensor}, outputs); err != nil {
		return nil, failure("OmniVoice could not make the sound: %v", err)
	}
	defer func() {
		if outputs[0] != nil {
			_ = outputs[0].Destroy()
		}
	}()

	samples, ok := outputs[0].(*ort.Tensor[float32])
	if !ok {
		return nil, failure("the decoder returned %T rather than audio", outputs[0])
	}
	return append([]float32(nil), samples.GetData()...), nil
}

// -- what comes out ------------------------------------------------------------

// postProcess matches the loudness to the reference recording and fades the
// ends.
//
// The fade is not cosmetic. Every utterance here is spliced into a stream of
// other utterances, and a waveform that starts at some arbitrary point in a
// cycle is heard as a click at the seam -- which on a stream is the kind of
// artefact that gets attributed to the encoder and chased for a week.
func postProcess(samples []float32, voice *Voice) []float32 {
	if len(samples) == 0 {
		return samples
	}

	if voice != nil && voice.RMS > 0 && voice.RMS < 0.1 {
		// Match a quiet reference rather than normalising to it: a voice
		// recorded softly should stay soft, or every prepared voice ends up
		// at the same loudness and the recording stops meaning anything.
		gain := voice.RMS / 0.1
		for index := range samples {
			samples[index] *= gain
		}
	} else if voice == nil {
		peak := float32(0)
		for _, value := range samples {
			if magnitude := float32(math.Abs(float64(value))); magnitude > peak {
				peak = magnitude
			}
		}
		if peak > 1e-6 {
			gain := 0.5 / peak
			for index := range samples {
				samples[index] *= gain
			}
		}
	}

	const fadeSeconds = 0.1
	fade := int(fadeSeconds * SampleRate)
	if fade > len(samples)/2 {
		fade = len(samples) / 2
	}
	for index := 0; index < fade; index++ {
		ramp := float32(index) / float32(fade)
		samples[index] *= ramp
		samples[len(samples)-1-index] *= ramp
	}
	return samples
}

// -- text ----------------------------------------------------------------------

// combineText is the text the model is asked to read, which is the reference
// transcript and the target run together.
//
// The tidying is the model's own: newlines out, full-width parentheses in,
// runs of space collapsed, and spaces next to Han characters removed, because
// a space between two Han characters is a typing artefact rather than a word
// boundary and the model hears it as a pause.
func combineText(text, referenceText string) string {
	full := strings.TrimSpace(text)
	if referenceText != "" {
		full = strings.TrimSpace(referenceText) + " " + full
	}

	full = strings.NewReplacer(
		"\r", "", "\n", "",
		"（", "(", "）", ")",
		"\t", " ",
	).Replace(full)

	for strings.Contains(full, "  ") {
		full = strings.ReplaceAll(full, "  ", " ")
	}
	return trimSpacesAroundHan(full)
}

func trimSpacesAroundHan(text string) string {
	runes := []rune(text)
	var out []rune
	for index, letter := range runes {
		if letter == ' ' {
			before := index > 0 && isHan(runes[index-1])
			after := index+1 < len(runes) && isHan(runes[index+1])
			if before || after {
				continue
			}
		}
		out = append(out, letter)
	}
	return string(out)
}

func isHan(letter rune) bool { return letter >= 0x4E00 && letter <= 0x9FFF }

// chunkLimit is how much text goes into one read.
//
// Shorter than Supertonic's, and for a different reason: there, a long read
// delays the start of playback. Here attention costs the square of the
// sequence length and the sequence grows with the audio being made, so the
// same words in one piece cost noticeably more than in three.
const chunkLimit = 160

// chunk splits text at sentence ends, and at the last space before the limit
// when a sentence runs past it.
func chunk(text string) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}

	var pieces []string
	for len(text) > chunkLimit {
		cut := lastBreak(text[:chunkLimit])
		if cut <= 0 {
			cut = chunkLimit
		}
		piece := strings.TrimSpace(text[:cut])
		if piece != "" {
			pieces = append(pieces, piece)
		}
		text = strings.TrimSpace(text[cut:])
	}
	if text != "" {
		pieces = append(pieces, text)
	}
	return pieces
}

// lastBreak is the best place to cut a piece: after the last sentence ending
// if there is one, otherwise after the last space.
func lastBreak(piece string) int {
	if at := strings.LastIndexAny(piece, ".!?;:。！？"); at > 0 {
		return at + 1
	}
	if at := strings.LastIndex(piece, " "); at > 0 {
		return at + 1
	}
	return 0
}

// -- voices --------------------------------------------------------------------

// Voice is a prepared reference: a recording already turned into codes, with
// the transcript and the loudness it was recorded at.
type Voice struct {
	Name  string    `json:"name"`
	Text  string    `json:"text"`
	RMS   float32   `json:"rms"`
	Codes [][]int32 `json:"codes"`
}

// Frames is how long the reference is, in audio frames.
func (v *Voice) Frames() int {
	if v == nil || len(v.Codes) == 0 {
		return 0
	}
	return len(v.Codes[0])
}

// voice loads one prepared voice, or returns the one already loaded.
//
// An empty name is not an error: it means no reference at all, which is a
// usable mode of the model rather than a missing setting.
func (e *Engine) voice(name string) (*Voice, error) {
	if name == "" {
		return nil, nil
	}
	if loaded, ok := e.voices[name]; ok {
		return loaded, nil
	}

	raw, err := os.ReadFile(preparedPath(name))
	if err != nil {
		return nil, failure("the voice %q is not prepared; see %s", name, voiceDir())
	}
	var loaded Voice
	if err := json.Unmarshal(raw, &loaded); err != nil {
		return nil, failure("could not read the voice %q: %v", name, err)
	}
	if len(loaded.Codes) != Codebooks || loaded.Frames() == 0 {
		return nil, failure("the voice %q is not shaped like a voice", name)
	}
	for _, row := range loaded.Codes {
		if len(row) != loaded.Frames() {
			return nil, failure("the voice %q has codebooks of different lengths", name)
		}
	}

	loaded.Name = name
	e.voices[name] = &loaded
	return &loaded, nil
}

func preparedPath(name string) string { return filepath.Join(voiceDir(), name+".json") }
func recordingPath(name string) string {
	return filepath.Join(voiceDir(), name+".wav")
}
func transcriptPath(name string) string { return filepath.Join(voiceDir(), name+".txt") }

// Voices lists what can be spoken in.
//
// Both the prepared voices and the recordings waiting to be prepared, because
// to somebody who has just dropped a wav in the folder those are the same
// thing: a voice that is there. Preparing it is this code's problem.
func Voices() []string {
	entries, err := os.ReadDir(voiceDir())
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var found []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		switch {
		case strings.HasSuffix(name, ".json"):
			name = strings.TrimSuffix(name, ".json")
		case strings.HasSuffix(name, ".wav"):
			name = strings.TrimSuffix(name, ".wav")
		default:
			continue
		}
		if !seen[name] {
			seen[name] = true
			found = append(found, name)
		}
	}
	sort.Strings(found)
	return found
}

// Known reports whether a name is a voice this engine could speak in.
func Known(name string) bool {
	if name == "" {
		return true // no reference at all is a valid choice
	}
	for _, found := range Voices() {
		if found == name {
			return true
		}
	}
	return false
}
