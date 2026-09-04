package tako

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/exzork/mikkilens/packages/core/config"
)

func tuning() config.Tako { return config.Default().Tako }

func number(value float64) *float64 { return &value }

// -- how long the quiet lasts --------------------------------------------------

func TestHoldForUsesTheDurationTheCreatorSet(t *testing.T) {
	var found alert
	found.Settings.Alert.Duration = number(6)

	// Six seconds on screen, a second before it starts, and the configured
	// margin on the end.
	if got, want := holdFor(found, tuning()), 9*time.Second; got != want {
		t.Errorf("held for %v, want %v", got, want)
	}
}

func TestHoldForFallsBackToTheOverlaysOwnDefault(t *testing.T) {
	// Tako's alert overlay uses ten seconds when the creator has set nothing,
	// and an empty settings block is what a fresh account sends.
	if got, want := holdFor(alert{}, tuning()), 13*time.Second; got != want {
		t.Errorf("held for %v, want %v", got, want)
	}
}

func TestHoldForCoversALongMessageBeingReadAloud(t *testing.T) {
	// An alert stays up until Tako has finished reading the donation, not for
	// its configured duration, so a long message has to extend the quiet or
	// chat comes back over the tail of it.
	var found alert
	found.Settings.Alert.Duration = number(6)
	found.Message = string(make([]rune, 140)) // 140 runes at 14 a second

	if got, want := holdFor(found, tuning()), 13*time.Second; got != want {
		t.Errorf("held for %v, want %v", got, want)
	}
}

func TestHoldForUsesTheVoiceNoteCap(t *testing.T) {
	// A recording runs for as long as it runs, and the overlay cuts it off at
	// the creator's cap. The cap is therefore the longest this can last.
	var found alert
	found.Settings.Alert.Duration = number(6)
	found.Settings.Alert.VNMaximumDuration = number(30)
	found.RecordingURL = "https://example.invalid/a-recording.mp3"

	if got, want := holdFor(found, tuning()), 33*time.Second; got != want {
		t.Errorf("held for %v, want %v", got, want)
	}
}

func TestHoldForIsCapped(t *testing.T) {
	var found alert
	found.Settings.Alert.Duration = number(4000)

	// Chat going quiet for the rest of a stream because a number came back
	// wrong is the failure this rules out.
	if got, want := holdFor(found, tuning()), 90*time.Second; got != want {
		t.Errorf("held for %v, want %v", got, want)
	}
}

// -- reading the overlay -------------------------------------------------------

const alertBody = `{"statusCode":200,"result":{
	"$":{"id":"gift-1","createdAt":"2026-09-01T00:00:00Z","isTest":false},
	"sender":{"name":"Budi"},"amount":50000,"message":"semangat terus!",
	"_overlaySettings":{"alert":{"duration":8}}}}`

const noAlertBody = `{"statusCode":206,"result":{
	"_overlayKey":"key","_overlaySettings":{"alert":{}}}}`

func TestFetchReadsAnAlertWithoutEverWriting(t *testing.T) {
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)

		// The endpoint checks it is talking to a browser source. Getting any of
		// these wrong is a 400 or a 404 rather than an alert, and the symptom
		// is chat talking over every donation.
		if r.Header.Get("X-Overlay-Key") != "key" {
			t.Errorf("missing overlay key header")
		}
		if r.Header.Get("X-Path") != "/overlay/alert" {
			t.Errorf("missing path header, got %q", r.Header.Get("X-Path"))
		}
		if _, present := r.Header["X-Queued-Gift-Ids"]; !present {
			t.Errorf("the queued-gift header has to be sent even when it is empty")
		}
		if r.Header.Get("Referer") == "" {
			t.Errorf("the endpoint answers 400 without a referer")
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(alertBody))
	}))
	defer server.Close()

	found, waiting, err := fetch(context.Background(), server.Client(), server.URL, "key")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !waiting {
		t.Fatal("an alert was waiting and fetch said there was none")
	}
	if found.Meta.ID != "gift-1" || found.Sender.Name != "Budi" {
		t.Errorf("read the wrong alert: %+v", found)
	}
	if got := holdFor(found, tuning()); got != 11*time.Second {
		t.Errorf("held for %v, want 11s", got)
	}

	// The PUT that says an alert has been played is what moves Tako's queue
	// on, and it belongs to the browser source. Sending it from here would
	// take donations off her stream.
	for _, method := range methods {
		if method != http.MethodGet {
			t.Fatalf("this package must only ever read, sent a %s", method)
		}
	}
}

func TestFetchTreatsPartialContentAsNothingWaiting(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte(noAlertBody))
	}))
	defer server.Close()

	_, waiting, err := fetch(context.Background(), server.Client(), server.URL, "key")
	if err != nil {
		t.Fatalf("a quiet overlay is not an error: %v", err)
	}
	if waiting {
		t.Error("nothing was waiting, so chat must not be held")
	}
}

func TestFetchExplainsAnUnknownOverlayKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	_, _, err := fetch(context.Background(), server.Client(), server.URL, "wrong")
	if err == nil {
		t.Fatal("an unrecognised key has to be an error, or it fails silently")
	}
}

// -- the watcher ---------------------------------------------------------------

func TestDonationsQueueBackToBackRatherThanOverlapping(t *testing.T) {
	watcher := New(Options{Settings: tuning(), Key: "key"})

	first := watcher.extend(10 * time.Second)
	second := watcher.extend(10 * time.Second)

	// The overlay plays a run of donations one after another. Two ten-second
	// alerts are twenty seconds of quiet, not ten.
	if gap := second.Sub(first); gap < 9*time.Second || gap > 11*time.Second {
		t.Errorf("the second donation extended the quiet by %v, want about 10s", gap)
	}
}

func TestAnAlertIsOnlyHeldForOnce(t *testing.T) {
	watcher := New(Options{Settings: tuning(), Key: "key"})

	// The relay event and the five-minute catch-up can both see the same
	// alert, and holding twice would double the quiet for one donation.
	if watcher.alreadySeen("gift-1") {
		t.Fatal("a new alert should not be reported as seen")
	}
	if !watcher.alreadySeen("gift-1") {
		t.Error("the same alert was held for twice")
	}
}

func TestSanitizeMatchesTheChannelTakoSubscribesTo(t *testing.T) {
	cases := map[string]string{
		"cxzw1yhjbv91kn1toidmlteq": "cxzw1yhjbv91kn1toidmlteq",
		"with space":               "withspace",
		"a/b?c":                    "abc",
		"!!!":                      "INVALID",
	}
	for given, want := range cases {
		if got := sanitize(given); got != want {
			t.Errorf("sanitize(%q) = %q, want %q", given, got, want)
		}
	}
}

func TestStartWithoutAKeyDoesNothing(t *testing.T) {
	watcher := New(Options{Settings: tuning()})
	watcher.Start()
	defer watcher.Stop()

	if watcher.Running() {
		t.Error("a watcher with no overlay key must not connect")
	}
}

// -- the dashboard's test button -----------------------------------------------

// Pressing Test is the first thing anyone does after setting this up, and it is
// the one alert that never reaches the donation queue: the overlay builds it in
// the browser, so no gift exists and no "messages" event is sent. It arrives as
// a command instead.

func heldBy(t *testing.T, payload string) (time.Time, string, bool) {
	t.Helper()

	var until time.Time
	var donor string
	held := false

	watcher := New(Options{
		Settings: tuning(),
		Key:      "key",
		OnHold: func(at time.Time, given Donation) {
			until, donor, held = at, given.Donor, true
		},
	})
	watcher.onCommand(json.RawMessage(`"` + payload + `"`))
	return until, donor, held
}

func TestTheDashboardTestButtonHoldsChat(t *testing.T) {
	until, donor, held := heldBy(t, "alert.test")
	if !held {
		t.Fatal("a test alert has to hold chat, or the check that proves this works proves nothing")
	}
	if donor != exampleDonor {
		t.Errorf("donor %q, want %q", donor, exampleDonor)
	}
	// No settings have been read, so the overlay's own ten-second default
	// applies: 10 + 1 lead-in + 2 margin.
	if wait := time.Until(until); wait < 12*time.Second || wait > 14*time.Second {
		t.Errorf("held for %v, want about 13s", wait)
	}
}

func TestAVoiceNoteTestIsHeldForTheCap(t *testing.T) {
	plain, _, _ := heldBy(t, "alert.test")
	voice, _, held := heldBy(t, "alert.test.vn")
	if !held {
		t.Fatal("a voice note test has to hold chat too")
	}
	// With no cap read from the overlay both fall back to the same default,
	// so the point here is only that the variant is understood rather than
	// dropped for having a third part.
	if voice.Before(plain.Add(-time.Second)) {
		t.Errorf("a voice note test was held for less than a plain one")
	}
}

func TestACommandForAnotherOverlayIsIgnored(t *testing.T) {
	// The same key carries mediashare and songshare. Testing one of those must
	// not silence chat for an alert that is not being shown.
	if _, _, held := heldBy(t, "mediashare.test"); held {
		t.Error("a mediashare test held chat")
	}
}

func TestTheOtherDashboardButtonsDoNotHoldChat(t *testing.T) {
	for _, payload := range []string{"alert.pause", "alert.play", "alert.skip", "alert.refresh", "*.pause"} {
		if _, _, held := heldBy(t, payload); held {
			t.Errorf("%q held chat; only a donation should", payload)
		}
	}
}

func TestAMalformedCommandIsIgnored(t *testing.T) {
	for _, payload := range []string{"", "alert", "nonsense"} {
		if _, _, held := heldBy(t, payload); held {
			t.Errorf("%q held chat", payload)
		}
	}
}

func TestParseLinkTakesTheWholeOverlayAddress(t *testing.T) {
	// The dashboard hands out a link; the config used to take only the key out
	// of the middle of it, and both have to keep working.
	link := "https://tako.id/overlay/alert?overlay_key=cxzw1yhjbv91kn1toidmlteq"
	got, err := ParseLink(link)
	if err != nil || got != "cxzw1yhjbv91kn1toidmlteq" {
		t.Errorf("ParseLink(link) = %q, %v", got, err)
	}

	bare, err := ParseLink("cxzw1yhjbv91kn1toidmlteq")
	if err != nil || bare != "cxzw1yhjbv91kn1toidmlteq" {
		t.Errorf("a bare key should still be accepted, got %q, %v", bare, err)
	}

	for _, bad := range []string{"", "   ", "https://tako.id/overlay/alert"} {
		if _, err := ParseLink(bad); err == nil {
			t.Errorf("ParseLink(%q) was accepted", bad)
		}
	}
}

func TestFormatAmountGroupsThousands(t *testing.T) {
	// "Rp10.000" is a number she can hear the size of; a run of digits is not.
	cases := map[string]string{
		"10000":  "Rp10.000",
		"5000":   "Rp5.000",
		"750":    "Rp750",
		"123456": "Rp123.456",
	}
	for given, want := range cases {
		amount := 0.0
		_, _ = fmt.Sscanf(given, "%f", &amount)
		if got := FormatAmount(amount, "idr"); got != want {
			t.Errorf("FormatAmount(%s, idr) = %q, want %q", given, got, want)
		}
	}
	if got := FormatAmount(25, "usd"); got != "25 USD" {
		t.Errorf("a foreign currency should be named, got %q", got)
	}
}
