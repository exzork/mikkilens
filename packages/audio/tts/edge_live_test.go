package tts

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestEdgeSynthesisLive talks to the real service, so it is skipped unless
// MIKKILENS_LIVE=1. The normal test run stays offline and fast; this one is
// how you check that the Edge protocol still works after Microsoft changes it.
func TestEdgeSynthesisLive(t *testing.T) {
	if os.Getenv("MIKKILENS_LIVE") != "1" {
		t.Skip("set MIKKILENS_LIVE=1 to exercise the online voice")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	raw, err := SynthesizeEdge(ctx,
		"Halo, ini contoh suara MikkiLens.", "id-ID-GadisNeural", "+0%")
	if err != nil {
		t.Fatalf("SynthesizeEdge: %v", err)
	}
	if len(raw) < 1000 {
		t.Fatalf("suspiciously little audio: %d bytes", len(raw))
	}

	audio, err := Decode(raw)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	t.Logf("%d bytes of mp3 decoded to %.2fs at %d Hz, %d channels",
		len(raw), audio.Duration(), audio.SampleRate, audio.Channels)
	if audio.Duration() < 0.5 {
		t.Errorf("decoded audio is only %.2fs", audio.Duration())
	}
	t.Logf("after trimming the padding: %.2fs", TrimSilence(audio).Duration())
}

// TestListVoicesLive checks the other half of the Edge API, which the settings
// page uses to fill the voice dropdown.
func TestListVoicesLive(t *testing.T) {
	if os.Getenv("MIKKILENS_LIVE") != "1" {
		t.Skip("set MIKKILENS_LIVE=1 to exercise the online voice")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	voices, err := ListVoices(ctx)
	if err != nil {
		t.Fatalf("ListVoices: %v", err)
	}
	indonesian := 0
	for _, voice := range voices {
		if len(voice.Locale) >= 2 && voice.Locale[:2] == "id" {
			indonesian++
		}
	}
	t.Logf("%d voices, %d of them Indonesian", len(voices), indonesian)
	if indonesian == 0 {
		t.Error("expected at least one Indonesian voice")
	}
}
