package trakteer

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/exzork/mikkilens/packages/controllers/pusher"
)

// TestTheRealTrakteerOverlayStillHasTheShapeWeExpect is the canary.
//
// Everything else in this package tests against replies this repository wrote,
// which proves the parsing and proves nothing about Trakteer. None of what it
// depends on is a published contract: the relay host and application key, the
// channel naming, the settings endpoint and the names of the fields in it all
// come from reading Trakteer's own overlay, and any of them can be changed
// without notice. The first sign would otherwise be MikkiLens reading chat
// over a donation on a live stream.
//
// It talks to the network, so it does not run by default. To run it:
//
//	MIKKILENS_LIVE_TRAKTEER_TEST="<the whole notification overlay link>" \
//	    go test ./packages/controllers/trakteer -run RealTrakteer -v
func TestTheRealTrakteerOverlayStillHasTheShapeWeExpect(t *testing.T) {
	raw := os.Getenv("MIKKILENS_LIVE_TRAKTEER_TEST")
	if raw == "" {
		t.Skip("set MIKKILENS_LIVE_TRAKTEER_TEST to your notification overlay link")
	}

	link, err := ParseLink(raw)
	if err != nil {
		t.Fatalf("the overlay link no longer has the shape we expect: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()

	// The settings endpoint still answers, and still carries the delay that
	// decides how long chat stays quiet.
	client := &http.Client{Timeout: 10 * time.Second}
	set, err := fetchSettings(ctx, client, apiOrigin, link.Key)
	if err != nil {
		t.Fatalf("the settings endpoint no longer answers the way the overlay asks: %v", err)
	}
	if set.Delay == "" {
		t.Error("nt_delay was missing; every hold would fall back to the default")
	}
	t.Logf("settings: delay=%qs tts=%q voice_note=%q media_share=%q -> a plain donation holds chat for %v",
		set.Delay, set.TTS, set.VoiceNote, set.MediaShare,
		holdFor(tip{Type: "new-tip-success", Quantity: 1}, set, tuning()))

	// The relay still takes a public subscription on both channels.
	ready := make(chan struct{})
	dropped := make(chan error, 1)
	go func() {
		dropped <- pusher.Listen(ctx, pusher.Options{
			Endpoint: relayEndpoint,
			Origin:   overlayOrigin,
			Channels: link.Channels(),
			OnReady:  func() { close(ready) },
			OnEvent: func(channel, event string, data json.RawMessage) {
				t.Logf("event on %s: %s %s", channel, event, clip(string(data), 200))
			},
		})
	}()

	select {
	case <-ready:
	case err := <-dropped:
		t.Fatalf("the relay refused the subscription: %v", err)
	case <-time.After(20 * time.Second):
		t.Fatal("the relay never accepted both channels")
	}

	// Long enough to outlast the ping, which is the part that keeps a quiet
	// stream connected.
	select {
	case err := <-dropped:
		t.Fatalf("the relay dropped the subscription: %v", err)
	case <-time.After(25 * time.Second):
	}
}

func clip(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "..."
}
