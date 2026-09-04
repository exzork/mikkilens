package trakteer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/exzork/mikkilens/packages/core/config"
)

func tuning() config.Trakteer { return config.Default().Trakteer }

const exampleLink = "https://stream.trakteer.id/notification/index.html" +
	"?key=trstream-RmrNb0oqllpSvbzp7mz5&unit=Cendol&mod=3&hash=ldzq4kbe8vx4nea7"

// -- the overlay link ----------------------------------------------------------

func TestParseLinkTakesBothHalvesOfTheAddress(t *testing.T) {
	link, err := ParseLink(exampleLink)
	if err != nil {
		t.Fatalf("ParseLink: %v", err)
	}
	if link.Key != "trstream-RmrNb0oqllpSvbzp7mz5" || link.Hash != "ldzq4kbe8vx4nea7" {
		t.Errorf("read %+v", link)
	}

	// The overlay listens on both: donations on the first, the dashboard's
	// test button on the second.
	want := []string{
		"creator-stream.ldzq4kbe8vx4nea7.trstream-RmrNb0oqllpSvbzp7mz5",
		"creator-stream-test.ldzq4kbe8vx4nea7.trstream-RmrNb0oqllpSvbzp7mz5",
	}
	got := link.Channels()
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("channels %v, want %v", got, want)
	}
}

func TestParseLinkRefusesAHalfAddress(t *testing.T) {
	// Half a link connects to a channel nobody broadcasts on, which is silent
	// in exactly the way a working setup is.
	for _, given := range []string{
		"", "not a url",
		"https://stream.trakteer.id/notification/?key=trstream-abc",
		"https://stream.trakteer.id/notification/?hash=ldzq4kbe8vx4nea7",
	} {
		if _, err := ParseLink(given); err == nil {
			t.Errorf("ParseLink(%q) was accepted", given)
		}
	}
}

// -- how long the quiet lasts --------------------------------------------------

func TestHoldForUsesTheCreatorsDelay(t *testing.T) {
	set := settings{Delay: "5"}
	if got, want := holdFor(tip{}, set, tuning()), 7*time.Second; got != want {
		t.Errorf("held for %v, want %v", got, want)
	}
}

func TestHoldForFallsBackToTheOverlaysOwnDefault(t *testing.T) {
	// Five seconds is what the overlay uses when nt_delay is unset.
	if got, want := holdFor(tip{}, settings{}, tuning()), 7*time.Second; got != want {
		t.Errorf("held for %v, want %v", got, want)
	}
}

func TestHoldForScalesAVoiceNoteWithTheUnitsGiven(t *testing.T) {
	// Trakteer plays one voice note length per unit, so ten Cendol is a much
	// longer alert than one.
	set := settings{
		Delay: "5", VoiceNote: "true", VoiceNoteDuration: "4",
		CapVoiceNote: "true", VoiceNoteMaxDuration: "30",
	}
	given := tip{Quantity: 3, Media: map[string]json.RawMessage{"voice": json.RawMessage(`"x"`)}}

	// 4s each for three units, plus the margin.
	if got, want := holdFor(given, set, tuning()), 14*time.Second; got != want {
		t.Errorf("held for %v, want %v", got, want)
	}
}

func TestHoldForRespectsTheVoiceNoteCap(t *testing.T) {
	set := settings{
		Delay: "5", VoiceNote: "true", VoiceNoteDuration: "10",
		CapVoiceNote: "true", VoiceNoteMaxDuration: "20",
	}
	given := tip{Quantity: 50, Media: map[string]json.RawMessage{"voice": json.RawMessage(`"x"`)}}

	if got, want := holdFor(given, set, tuning()), 22*time.Second; got != want {
		t.Errorf("held for %v, want %v", got, want)
	}
}

func TestHoldForIgnoresMediaTheCreatorHasSwitchedOff(t *testing.T) {
	// The donation carries a voice note but the overlay will not play it, so
	// the alert is only up for the ordinary delay.
	set := settings{Delay: "5", VoiceNote: "false", VoiceNoteDuration: "30"}
	given := tip{Quantity: 1, Media: map[string]json.RawMessage{"voice": json.RawMessage(`"x"`)}}

	if got, want := holdFor(given, set, tuning()), 7*time.Second; got != want {
		t.Errorf("held for %v, want %v", got, want)
	}
}

func TestHoldForCoversAMessageBeingReadAloud(t *testing.T) {
	set := settings{Delay: "5", TTS: "true", TTSMaxDuration: "60"}
	given := tip{SupporterMessage: string(make([]rune, 140))} // 140 runes at 14 a second

	if got, want := holdFor(given, set, tuning()), 12*time.Second; got != want {
		t.Errorf("held for %v, want %v", got, want)
	}
}

func TestHoldForStopsAtTrakteersOwnTTSLimit(t *testing.T) {
	set := settings{Delay: "5", TTS: "true", TTSMaxDuration: "8"}
	given := tip{SupporterMessage: string(make([]rune, 1400))}

	// Trakteer gives up reading at its own limit, so the alert cannot run on
	// for as long as the message would take.
	if got, want := holdFor(given, set, tuning()), 10*time.Second; got != want {
		t.Errorf("held for %v, want %v", got, want)
	}
}

func TestHoldForIsCapped(t *testing.T) {
	set := settings{Delay: "4000"}
	if got, want := holdFor(tip{}, set, tuning()), 90*time.Second; got != want {
		t.Errorf("held for %v, want %v", got, want)
	}
}

func TestNonsenseSettingsFallBackRatherThanCollapsing(t *testing.T) {
	// An empty or unparseable delay must not turn into a zero-length hold,
	// which would read as the feature doing nothing.
	for _, given := range []string{"", "abc", "0", "-3"} {
		if got := holdFor(tip{}, settings{Delay: given}, tuning()); got != 7*time.Second {
			t.Errorf("delay %q held for %v, want 7s", given, got)
		}
	}
}

// -- what counts as a donation -------------------------------------------------

func TestOnlyTipsHoldChat(t *testing.T) {
	real := []string{"new-tip-success", "new-tip-success-approved", "new-tip-replay"}
	for _, kind := range real {
		given := tip{Type: kind}
		if !given.isTip() || !given.isReal() {
			t.Errorf("%q should be a real donation", kind)
		}
	}
	if given := (tip{Type: "new-tip-simulation"}); !given.isTip() || given.isReal() {
		t.Error("a simulation should hold chat but not count as real")
	}
	// Goals and leaderboards ride the same channels and never go on screen as
	// an alert.
	for _, kind := range []string{"goal-updated", "leaderboard-updated", ""} {
		if (tip{Type: kind}).isTip() {
			t.Errorf("%q should not hold chat", kind)
		}
	}
}

// -- reading the settings ------------------------------------------------------

const settingsBody = `{"settings":[{"nt_delay":"5","nt_tts":"false",
	"nt_voice_note":"false","nt_media_share":"false","nt_tts_max_duration":"60"}]}`

func TestFetchSettingsReadsTheCreatorsConfiguration(t *testing.T) {
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		if r.URL.Path != "/v2/stream/trstream-abc/settings/nt" {
			t.Errorf("asked for %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(settingsBody))
	}))
	defer server.Close()

	set, err := fetchSettings(context.Background(), server.Client(), server.URL, "trstream-abc")
	if err != nil {
		t.Fatalf("fetchSettings: %v", err)
	}
	if set.Delay != "5" {
		t.Errorf("delay %q, want 5", set.Delay)
	}
	for _, method := range methods {
		if method != http.MethodGet {
			t.Fatalf("this package must only ever read, sent a %s", method)
		}
	}
}

func TestFetchSettingsExplainsAnUnknownKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	if _, err := fetchSettings(context.Background(), server.Client(), server.URL, "wrong"); err == nil {
		t.Fatal("an unrecognised key has to be an error, or it fails silently")
	}
}

// -- the watcher ---------------------------------------------------------------

func TestADonationHoldsChat(t *testing.T) {
	var donor string
	var until time.Time
	held := false

	watcher := New(Options{
		Settings: tuning(),
		Link:     Link{Key: "k", Hash: "h"},
		OnHold:   func(at time.Time, given Donation) { until, donor, held = at, given.Donor, true },
	})
	watcher.seenSettings = settings{Delay: "5"}

	payload, _ := json.Marshal(map[string]any{
		"type": "new-tip-success", "tip_id": "tip-1",
		"supporter_name": "Budi", "unit": "Cendol", "quantity": 1,
	})
	watcher.onEvent(notificationEvent, payload)

	if !held {
		t.Fatal("a donation has to hold chat")
	}
	if donor != "Budi" {
		t.Errorf("donor %q, want Budi", donor)
	}
	if wait := time.Until(until); wait < 6*time.Second || wait > 8*time.Second {
		t.Errorf("held for %v, want about 7s", wait)
	}
}

func TestTheSameDonationIsOnlyHeldForOnce(t *testing.T) {
	holds := 0
	watcher := New(Options{
		Settings: tuning(),
		Link:     Link{Key: "k", Hash: "h"},
		OnHold:   func(time.Time, Donation) { holds++ },
	})

	// It arrives on the real channel and the test one both, and holding twice
	// would double the quiet for one donation.
	payload, _ := json.Marshal(map[string]any{
		"type": "new-tip-success", "tip_id": "tip-1", "supporter_name": "Budi",
	})
	watcher.onEvent(notificationEvent, payload)
	watcher.onEvent(notificationEvent, payload)

	if holds != 1 {
		t.Errorf("held %d times for one donation, want 1", holds)
	}
}

func TestAnotherEventOnTheChannelIsIgnored(t *testing.T) {
	held := false
	watcher := New(Options{
		Settings: tuning(),
		Link:     Link{Key: "k", Hash: "h"},
		OnHold:   func(time.Time, Donation) { held = true },
	})

	watcher.onEvent("some-other-event", json.RawMessage(`{}`))
	payload, _ := json.Marshal(map[string]any{"type": "goal-updated", "tip_id": "g-1"})
	watcher.onEvent(notificationEvent, payload)

	if held {
		t.Error("something that is not a donation held chat")
	}
}

func TestAPayloadWrappedAsAStringIsStillUnderstood(t *testing.T) {
	// Pusher sends the payload as a JSON string containing JSON. Throwing that
	// away as malformed would mean never hearing about a donation at all.
	held := false
	watcher := New(Options{
		Settings: tuning(),
		Link:     Link{Key: "k", Hash: "h"},
		OnHold:   func(time.Time, Donation) { held = true },
	})

	inner, _ := json.Marshal(map[string]any{
		"type": "new-tip-success", "tip_id": "tip-9", "supporter_name": "Sari",
	})
	wrapped, _ := json.Marshal(string(inner))
	watcher.onEvent(notificationEvent, wrapped)

	if !held {
		t.Error("a string-wrapped payload was dropped")
	}
}

func TestDonationsQueueBackToBackRatherThanOverlapping(t *testing.T) {
	watcher := New(Options{Settings: tuning(), Link: Link{Key: "k", Hash: "h"}})

	first := watcher.extend(10 * time.Second)
	second := watcher.extend(10 * time.Second)

	if gap := second.Sub(first); gap < 9*time.Second || gap > 11*time.Second {
		t.Errorf("the second donation extended the quiet by %v, want about 10s", gap)
	}
}

func TestStartWithoutALinkDoesNothing(t *testing.T) {
	watcher := New(Options{Settings: tuning()})
	watcher.Start()
	defer watcher.Stop()

	if watcher.Running() {
		t.Error("a watcher with no overlay link must not connect")
	}
}

// -- what Trakteer actually sends ----------------------------------------------

// capturedTestAlert is a real frame, captured off the relay when the Trakteer
// dashboard's test button was pressed.
//
// It is here because two things about it were guessed wrong from reading the
// overlay's source, and both failed silently: the event name carries no
// leading dot, and the donation's identifier is "id" rather than "tip_id".
// Nothing about either is documented, so the only guard against guessing again
// is a copy of the real thing.
const capturedTestAlert = `{
	"supporter_name": "[ Stream Test ]",
	"unit": "Cendol",
	"quantity": 1,
	"supporter_message": "Selalu Berkarya!",
	"supporter_avatar": "https://edge-cdn.trakteer.id/images/mix/default-avatar.png?v=14-05-2025",
	"unit_icon": "https://cdn.trakteer.id/images/mix/cendol.png",
	"price": "Rp 5.000",
	"media": null,
	"preset_id": "preset_qnwmkv5704blag6r",
	"id": "e95d3cc9-1f6a-4a65-96ae-de8805b9e7db",
	"type": "new-tip-simulation"
}`

const capturedEventName = `Illuminate\Notifications\Events\BroadcastNotificationCreated`

func TestTheEventNameTrakteerActuallySendsIsRecognised(t *testing.T) {
	// Laravel Echo is asked for ".Illuminate\..." and strips the dot before
	// subscribing, so the dotted spelling never appears on the wire. Matching
	// it matched nothing, and nothing is what a working integration also looks
	// like.
	if !isNotification(capturedEventName) {
		t.Error("the event name Trakteer sends was not recognised")
	}
	if !isNotification("." + capturedEventName) {
		t.Error("Echo's dotted spelling should be understood too")
	}
	if isNotification("some-other-event") {
		t.Error("an unrelated event was treated as a notification")
	}
}

func TestACapturedTrakteerAlertHoldsChat(t *testing.T) {
	var donor string
	var until time.Time
	held := false

	watcher := New(Options{
		Settings: tuning(),
		Link:     Link{Key: "k", Hash: "h"},
		OnHold:   func(at time.Time, given Donation) { until, donor, held = at, given.Donor, true },
	})
	watcher.seenSettings = settings{Delay: "5"}

	watcher.onEvent(capturedEventName, json.RawMessage(capturedTestAlert))

	if !held {
		t.Fatal("the frame Trakteer really sends did not hold chat")
	}
	if donor != "[ Stream Test ]" {
		t.Errorf("donor %q", donor)
	}
	if wait := time.Until(until); wait < 6*time.Second || wait > 8*time.Second {
		t.Errorf("held for %v, want about 7s", wait)
	}
}

func TestACapturedAlertIsDedupedByItsRealIdField(t *testing.T) {
	holds := 0
	watcher := New(Options{
		Settings: tuning(),
		Link:     Link{Key: "k", Hash: "h"},
		OnHold:   func(time.Time, Donation) { holds++ },
	})

	// Keyed on "tip_id", every donation looked new, so one arriving on both
	// channels would have been held for twice.
	watcher.onEvent(capturedEventName, json.RawMessage(capturedTestAlert))
	watcher.onEvent(capturedEventName, json.RawMessage(capturedTestAlert))

	if holds != 1 {
		t.Errorf("held %d times for one donation, want 1", holds)
	}
}

func TestNullMediaIsNotMistakenForAVoiceNote(t *testing.T) {
	// Trakteer sends "media": null rather than leaving it out, and reading
	// that as a voice note would hold chat for the voice-note length on every
	// plain donation.
	var given tip
	if err := json.Unmarshal([]byte(capturedTestAlert), &given); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if given.has("voice") || given.has("video") {
		t.Error("null media was read as carrying something")
	}
}
