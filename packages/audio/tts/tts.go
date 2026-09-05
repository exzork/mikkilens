// Package tts turns text into audio, and audio into sound on a chosen device.
//
// The online Edge voices sound natural in Indonesian and cost nothing, but
// they need the network. A dropped connection must never become silence, so
// the offline Windows voice sits behind them.
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
	Voice string
	Rate  string

	// NoCache skips both cache layers. Chat sets it: those phrases are never
	// repeated, so caching them only evicts the ones that are.
	NoCache bool

	// NoTrim keeps the silence the online voice pads each phrase with.
	NoTrim bool
}

// Synthesize renders text, preferring the online voice and falling back to the
// offline one.
//
// Offline results are deliberately never cached. They are a degraded
// substitute, and caching them would keep the wrong voice long after the
// network came back.
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

	raw, err := SynthesizeEdge(ctx, text, options.Voice, options.Rate)
	if err != nil {
		slog.Warn("online voice failed, using the offline Windows voice", "error", err)
		audio, offlineErr := SynthesizeSAPI(ctx, text, parsePercent(options.Rate))
		if offlineErr != nil {
			return Audio{}, offlineErr
		}
		if !options.NoTrim {
			audio = TrimSilence(audio)
		}
		audio.Text = text
		return audio, nil
	}

	audio, err := Decode(raw)
	if err != nil {
		return Audio{}, err
	}
	if !options.NoTrim {
		audio = TrimSilence(audio)
	}
	audio.Text = text

	if !options.NoCache {
		storeOnDisk(key, raw)
		remember(key, audio)
	}
	return audio, nil
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
		if strings.HasSuffix(entry.Name(), ".mp3") {
			_ = os.Remove(filepath.Join(paths.TTSCacheDir(), entry.Name()))
		}
	}
}

// -- caching ------------------------------------------------------------------

func cacheKey(text string, options Options) string {
	sum := sha256.Sum256([]byte(strings.Join(
		[]string{text, options.Voice, options.Rate}, "\x00")))
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

func diskPath(key string) string {
	return filepath.Join(paths.TTSCacheDir(), key+".mp3")
}

func recallFromDisk(key string) (Audio, bool) {
	data, err := os.ReadFile(diskPath(key))
	if err != nil {
		return Audio{}, false
	}
	audio, err := Decode(data)
	if err != nil {
		// A truncated cache entry is worse than none: drop it so the next
		// attempt re-synthesizes rather than failing forever.
		_ = os.Remove(diskPath(key))
		return Audio{}, false
	}
	return audio, true
}

func storeOnDisk(key string, raw []byte) {
	if err := os.MkdirAll(paths.TTSCacheDir(), 0o755); err != nil {
		slog.Debug("could not create the speech cache", "error", err)
		return
	}
	if err := os.WriteFile(diskPath(key), raw, 0o644); err != nil {
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
