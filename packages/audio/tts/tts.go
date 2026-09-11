// Package tts turns text into audio, and audio into sound on a chosen device.
//
// There are four voices, and the rule between them is that a dropped
// connection must never become silence:
//
//	local      Supertonic 3, run here through ONNX Runtime. Thirty-one
//	           languages, ten voices, and nothing outside this machine to
//	           depend on. The default, and four hundred megabytes to fetch.
//	online     Edge TTS, Microsoft's neural voices. Free and natural, but
//	           they need the network, they need the clock to be roughly
//	           right, and they are somebody else's service to withdraw.
//	windows    SAPI 5, the synthesizer built into Windows. No download, no
//	           network, and it sounds like it -- the floor, not a choice.
//	omnivoice  OmniVoice, also run here. Six hundred languages, and its
//	           voice comes from a recording rather than a list. Two
//	           gigabytes, and it wants a graphics card: on one it is faster
//	           than real time, on a processor alone it is twenty-five times
//	           slower than real time.
//
// Whichever she picks, the others stand behind it -- except OmniVoice, which
// stands behind nothing. Falling back is for when something has gone wrong and
// the answer still has to arrive; on a machine with no card, an engine that
// takes half a minute a sentence is not an answer arriving, it is the same
// silence with extra steps.
//
// A substitute is never cached: it is a degraded answer to a temporary problem,
// and keeping it would hold the wrong voice long after the right one came
// back.
package tts

import (
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/exzork/mikkilens/packages/core/paths"
)

// Feedback phrases repeat constantly ("Mikrofon dimatikan.", "Kamu sudah
// live."), and the online voice costs about a second each time. Caching them
// turns almost every confirmation into an instant response. Chat messages are
// unique and miss the cache, which is fine: they are far less latency
// critical than an acknowledgement she is waiting on.
const memoryCacheSize = 64

var (
	cacheMu     sync.Mutex
	cacheOrder  = list.New()
	cacheByKey  = map[string]*list.Element{}
	cacheLookup = map[string]Audio{}
)

// Options are the knobs on one piece of synthesis.
//
// Loudness is deliberately not one of them. The online voice is asked for its
// own level every time and the volume is applied to the samples afterwards, by
// [Audio.AtVolume] -- see the comment there for why.
type Options struct {
	// Engine is which voice reads: EngineLocal, EngineOnline, EngineWindows
	// or EngineOmni. Empty means local.
	Engine string

	// Voice is a name in whatever scheme Engine uses -- "F1" for the local
	// voice, "id-ID-GadisNeural" for the online one, the name of a recording
	// for OmniVoice. A name the chosen engine does not recognise falls back to
	// that engine's default rather than failing, because a voice she did not
	// pick is a better answer than silence.
	Voice string

	Rate string

	// Language is what the text is in, as a two-letter code. The local voice
	// needs it: the tag is read as part of the text, and the same words under
	// the wrong tag come out with the wrong vowels. The online voice takes the
	// language from its voice name instead and ignores this.
	Language string

	// OnlineVoice is the online voice for this language, which is not the same
	// naming scheme as the local one. It is what the online voice reads in when
	// it is standing in for a local voice that could not run, so a missing
	// download does not also mean a voice from the wrong language.
	OnlineVoice string

	// NoCache skips both cache layers. Chat sets it: those phrases are never
	// repeated, so caching them only evicts the ones that are.
	NoCache bool

	// NoTrim keeps the silence the online voice pads each phrase with.
	NoTrim bool
}

// Synthesize renders text with the chosen voice, falling back through the
// others rather than failing.
func Synthesize(ctx context.Context, text string, options Options) (Audio, error) {
	key := cacheKey(text, options)

	if !options.NoCache {
		if cached, ok := recall(key); ok {
			cached.Text = text
			return cached, nil
		}
		if audio, ok := recallFromDisk(key); ok {
			if !options.NoTrim {
				audio = TrimSilence(audio)
			}
			audio.Text = text
			remember(key, audio)
			return audio, nil
		}
	}

	audio, encoded, err := render(ctx, text, options)
	if err != nil {
		return Audio{}, err
	}
	if !options.NoTrim {
		audio = TrimSilence(audio)
	}
	audio.Text = text

	// encoded is nil when a substitute answered. See the package comment.
	if !options.NoCache && encoded != nil {
		storeOnDisk(key, encoded)
		remember(key, audio)
	}
	return audio, nil
}

// render walks the engines, starting with the one she chose.
//
// The error reported when all of them fail is the first one, because that is
// the voice she asked for and the only one worth telling her about. "The
// Windows synthesizer is unavailable" is a true and useless thing to say to
// somebody whose network is down.
func render(ctx context.Context, text string, options Options) (Audio, []byte, error) {
	var first error

	for at, engine := range fallbackOrder(resolveEngine(options.Engine)) {
		audio, encoded, err := renderWith(ctx, engine, text, options)
		if err == nil {
			if at == 0 {
				return audio, encoded, nil
			}
			return audio, nil, nil
		}
		// Being cut off is not a voice failing. Working down the list on a
		// cancelled context would mean an interrupted utterance was attempted
		// three times before it stopped.
		if ctx.Err() != nil {
			return Audio{}, nil, err
		}
		if at == 0 {
			first = err
			slog.Warn("the chosen voice failed; trying the next one",
				"engine", engine, "error", err)
		}
	}
	return Audio{}, nil, first
}

// fallbackOrder is who stands in for whom.
//
// Windows has nothing behind it because it is already the floor, and nothing
// in front of it either when she has chosen it: picking the plain voice on
// purpose and being given a different one anyway is not a fallback, it is the
// setting being ignored.
func fallbackOrder(chosen string) []string {
	switch chosen {
	case EngineOnline:
		return []string{EngineOnline, EngineLocal, EngineWindows}
	case EngineWindows:
		return []string{EngineWindows}
	case EngineOmni:
		// Everything stands behind OmniVoice and OmniVoice stands behind
		// nothing. Without a graphics card it is the slowest by a wide margin,
		// so arriving at it by accident -- because a download had not
		// finished, or the network was down for a moment -- would turn a
		// missing confirmation into a confirmation that comes half a minute
		// late, which on a live stream is worse than the one that never came.
		return []string{EngineOmni, EngineLocal, EngineOnline, EngineWindows}
	default:
		return []string{EngineLocal, EngineOnline, EngineWindows}
	}
}

// renderWith is one engine's attempt. The bytes it returns are what would go in
// the disk cache, and are nil for the Windows voice, which is never cached.
func renderWith(ctx context.Context, engine, text string, options Options) (Audio, []byte, error) {
	switch engine {
	case EngineLocal:
		audio, err := synthesizeLocal(ctx, text, options)
		if err != nil {
			return Audio{}, nil, err
		}
		return audio, EncodeWAV(audio), nil

	case EngineOmni:
		audio, err := synthesizeOmni(ctx, text, options)
		if err != nil {
			return Audio{}, nil, err
		}
		return audio, EncodeWAV(audio), nil

	case EngineOnline:
		raw, err := SynthesizeEdge(ctx, text, onlineVoice(options), options.Rate)
		if err != nil {
			return Audio{}, nil, err
		}
		audio, err := Decode(raw)
		if err != nil {
			return Audio{}, nil, err
		}
		return audio, raw, nil

	default:
		audio, err := SynthesizeSAPI(ctx, text, parsePercent(options.Rate))
		if err != nil {
			return Audio{}, nil, err
		}
		return audio, nil, nil
	}
}

// onlineVoice picks a name the Edge service will accept.
//
// Every Edge voice is "language-REGION-NameNeural" and no local voice has a
// dash in it, so the shape of the string is enough to tell whether the
// configured voice belongs to this service. When it does not -- the local
// voice is the one she chose, and this is standing in for it -- the language
// default takes over.
func onlineVoice(options Options) string {
	if strings.Contains(options.Voice, "-") {
		return options.Voice
	}
	if options.OnlineVoice != "" {
		return options.OnlineVoice
	}
	return "en-US-AriaNeural"
}

// Prewarm synthesizes phrases ahead of time so the first use of each is
// instant. It never fails: a phrase that cannot be warmed is simply slow later.
func Prewarm(ctx context.Context, phrases []string, options Options) int {
	warmed := 0
	for _, phrase := range phrases {
		if _, err := Synthesize(ctx, phrase, options); err != nil {
			slog.Debug("could not prewarm a phrase", "phrase", phrase, "error", err)
			continue
		}
		warmed++
	}
	return warmed
}

// ClearCache drops cached speech. The voice or the rate changing makes every
// cached phrase wrong, so both call this.
func ClearCache() {
	cacheMu.Lock()
	cacheOrder = list.New()
	cacheByKey = map[string]*list.Element{}
	cacheLookup = map[string]Audio{}
	cacheMu.Unlock()

	entries, err := os.ReadDir(paths.TTSCacheDir())
	if err != nil {
		return
	}
	for _, entry := range entries {
		for _, extension := range cacheExtensions {
			if strings.HasSuffix(entry.Name(), extension) {
				_ = os.Remove(filepath.Join(paths.TTSCacheDir(), entry.Name()))
				break
			}
		}
	}
}

// -- caching ------------------------------------------------------------------

// cacheKey covers everything that changes what was rendered. The engine and
// the language are in here because the same words in the same voice name mean
// different audio under a different engine, and a stale entry would be heard
// as the setting having done nothing.
func cacheKey(text string, options Options) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		text, resolveEngine(options.Engine), options.Voice,
		options.Rate, options.Language,
	}, "\x00")))
	return hex.EncodeToString(sum[:])[:32]
}

func recall(key string) (Audio, bool) {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	element, ok := cacheByKey[key]
	if !ok {
		return Audio{}, false
	}
	cacheOrder.MoveToFront(element)
	return cacheLookup[key], true
}

func remember(key string, audio Audio) {
	cacheMu.Lock()
	defer cacheMu.Unlock()

	if element, ok := cacheByKey[key]; ok {
		cacheOrder.MoveToFront(element)
		cacheLookup[key] = audio
		return
	}
	cacheByKey[key] = cacheOrder.PushFront(key)
	cacheLookup[key] = audio

	for cacheOrder.Len() > memoryCacheSize {
		oldest := cacheOrder.Back()
		if oldest == nil {
			break
		}
		cacheOrder.Remove(oldest)
		evicted := oldest.Value.(string)
		delete(cacheByKey, evicted)
		delete(cacheLookup, evicted)
	}
}

// cacheExtensions are what a stored phrase can be. The online voice hands over
// MP3 and the local one PCM, and re-encoding either to match the other would be
// work done to make a filename tidier. Decode sniffs the content regardless;
// only the name has to be guessed.
var cacheExtensions = []string{".mp3", ".wav"}

func diskPath(key, extension string) string {
	return filepath.Join(paths.TTSCacheDir(), key+extension)
}

func recallFromDisk(key string) (Audio, bool) {
	for _, extension := range cacheExtensions {
		path := diskPath(key, extension)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		audio, err := Decode(data)
		if err != nil {
			// A truncated cache entry is worse than none: drop it so the next
			// attempt re-synthesizes rather than failing forever.
			_ = os.Remove(path)
			continue
		}
		return audio, true
	}
	return Audio{}, false
}

func storeOnDisk(key string, raw []byte) {
	if err := os.MkdirAll(paths.TTSCacheDir(), 0o755); err != nil {
		slog.Debug("could not create the speech cache", "error", err)
		return
	}
	extension := ".mp3"
	if len(raw) >= 4 && string(raw[0:4]) == "RIFF" {
		extension = ".wav"
	}
	if err := os.WriteFile(diskPath(key, extension), raw, 0o644); err != nil {
		slog.Debug("could not write the speech cache", "error", err)
	}
}

// parsePercent reads "+15%" as 15. Anything unparseable is simply no change,
// because a malformed rate must not stop her being spoken to.
func parsePercent(value string) int {
	cleaned := strings.TrimSpace(strings.NewReplacer("%", "", "+", "").Replace(value))
	if cleaned == "" {
		return 0
	}
	parsed, err := strconv.Atoi(cleaned)
	if err != nil {
		return 0
	}
	return parsed
}
