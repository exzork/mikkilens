package tts

import (
	"context"
	"os"
	"runtime"
	"testing"
	"time"
)

// TestTheLocalVoiceGoesThroughSynthesizeLive exercises the whole path the
// speech bus uses: choose an engine, render, trim, encode, store, and find it
// again. The pieces are tested apart from each other; this is the one that
// would catch them being wired together wrongly.
func TestTheLocalVoiceGoesThroughSynthesizeLive(t *testing.T) {
	if os.Getenv("MIKKILENS_LIVE") != "1" {
		t.Skip("set MIKKILENS_LIVE=1 to exercise the local voice")
	}
	if !LocalInstalled() {
		t.Skip("the local voice is not installed")
	}

	options := Options{Engine: EngineLocal, Voice: "F1", Language: "id", Rate: "+0%"}
	const line = "Kamu sudah live, dan mikrofonnya menyala."

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	ClearCache()

	cold := time.Now()
	first, err := Synthesize(ctx, line, options)
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	coldTook := time.Since(cold)

	if first.SampleRate != 44100 {
		t.Errorf("sample rate %d, want 44100", first.SampleRate)
	}
	if first.Channels != 1 {
		t.Errorf("%d channels, want 1", first.Channels)
	}
	if first.Text != line {
		t.Errorf("text = %q, want %q", first.Text, line)
	}
	if first.Duration() < 1 {
		t.Errorf("only %.2fs of audio", first.Duration())
	}
	t.Logf("cold: %.2fs of audio in %s", first.Duration(), coldTook.Round(time.Millisecond))

	// The second time has to come from the cache, and has to be the same
	// audio. A cache that returns something subtly different is worse than no
	// cache, because it only shows up on the phrases she hears most.
	warm := time.Now()
	second, err := Synthesize(ctx, line, options)
	if err != nil {
		t.Fatalf("Synthesize (cached): %v", err)
	}
	warmTook := time.Since(warm)
	t.Logf("warm: %s", warmTook.Round(time.Microsecond))

	if warmTook > coldTook/4 {
		t.Errorf("the second call took %s against %s; it does not look cached",
			warmTook, coldTook)
	}
	if len(second.Samples) != len(first.Samples) {
		t.Errorf("cached audio is %d samples against %d", len(second.Samples), len(first.Samples))
	}

	// And once more with the in-memory copy dropped, so what is read back is
	// the file on disk rather than the buffer it was written from.
	dropMemoryCache()
	third, err := Synthesize(ctx, line, options)
	if err != nil {
		t.Fatalf("Synthesize (from disk): %v", err)
	}
	if len(third.Samples) != len(first.Samples) {
		t.Errorf("audio from disk is %d samples against %d",
			len(third.Samples), len(first.Samples))
	}
	t.Logf("read back from disk: %.2fs", third.Duration())

	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	t.Logf("Go heap in use: %d MB (the models themselves are held by the "+
		"runtime, outside this)", memory.HeapInuse/1024/1024)
}

// dropMemoryCache empties the in-memory half only, leaving the files, so the
// disk path can be exercised on its own.
func dropMemoryCache() {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	for key := range cacheLookup {
		delete(cacheLookup, key)
	}
	for key := range cacheByKey {
		delete(cacheByKey, key)
	}
	cacheOrder.Init()
}
