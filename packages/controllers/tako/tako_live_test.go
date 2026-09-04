package tako

import (
	"context"
	"encoding/json"
	"net/http"
	"os"

	"github.com/exzork/mikkilens/packages/controllers/pusher"
	"testing"
	"time"
)

// TestTheRealTakoOverlayStillHasTheShapeWeExpect is the canary.
//
// Everything else in this package tests against replies this repository wrote,
// which proves the parsing and proves nothing about Tako. None of what it
// depends on is a published contract: the relay host, the channel naming, the
// headers the overlay endpoint insists on and the shape of what it returns all
// come from reading Tako's own overlay, and any of them can be changed without
// notice. The first sign would otherwise be MikkiLens reading chat over a
// donation on a live stream, which is exactly the thing this exists to stop.
//
// It talks to the network, so it does not run by default. To run it:
//
//	MIKKILENS_LIVE_TAKO_TEST=<the overlay_key from your alert overlay link> \
//	    go test ./packages/controllers/tako -run RealTako -v
func TestTheRealTakoOverlayStillHasTheShapeWeExpect(t *testing.T) {
	key := os.Getenv("MIKKILENS_LIVE_TAKO_TEST")
	if key == "" {
		t.Skip("set MIKKILENS_LIVE_TAKO_TEST to the overlay_key from your alert overlay link")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()

	// The endpoint still answers this client, and still tells the two cases
	// apart: "here is an alert" and "nothing is waiting".
	client := &http.Client{Timeout: 10 * time.Second}
	found, waiting, err := fetch(ctx, client, overlayOrigin, key)
	if err != nil {
		t.Fatalf("the overlay endpoint no longer answers the way the browser source asks: %v", err)
	}
	duration := "unset, so the overlay's own default"
	if set := found.Settings.Alert.Duration; set != nil {
		duration = time.Duration(*set * float64(time.Second)).String()
	}
	t.Logf("overlay answered: alert waiting=%v, configured duration=%s", waiting, duration)

	// The relay still takes a public subscription on the overlay channel. If
	// this stops working, donations stop being noticed at all.
	dropped := make(chan error, 1)
	go func() {
		dropped <- pusher.Listen(ctx, pusher.Options{
			Endpoint: relayEndpoint,
			Origin:   overlayOrigin,
			Channels: []string{"overlay." + sanitize(key)},
			OnEvent: func(_, event string, _ json.RawMessage) {
				t.Logf("relay event: %s", event)
			},
		})
	}()

	// A refused subscription or a protocol Tako has moved on from comes back
	// fast, so staying up is the signal. Long enough to outlast the ping, which
	// is the part that keeps a quiet stream connected.
	select {
	case err := <-dropped:
		t.Fatalf("the relay dropped the subscription: %v", err)
	case <-time.After(25 * time.Second):
	}
}
