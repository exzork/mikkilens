package tako

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/exzork/mikkilens/packages/controllers/pusher"
	"github.com/exzork/mikkilens/packages/core/config"
)

// The overlay endpoint answers the browser source in OBS, and it checks that
// it is talking to one: without a referer it returns 400, and the queued-gift
// header has to be present even when it is empty.
const (
	overlayOrigin = "https://tako.id"
	overlayPath   = "/overlay/alert"
	browserAgent  = pusher.BrowserAgent

	// What the alert overlay does with a donation, taken from the overlay's own
	// code so that the quiet lasts as long as the alert does: it waits a second
	// before starting, and falls back to ten seconds when the creator has not
	// set a duration.
	alertLeadIn          = 1 * time.Second
	defaultAlertDuration = 10 * time.Second
)

// statusNoAlert is Tako's "here is the overlay configuration, and nothing is
// waiting" -- a 206 rather than an empty 200.
const statusNoAlert = http.StatusPartialContent

// Tako's realtime relay speaks Pusher's protocol on its own host, which is why
// there is no cluster in the address. The application key names the software
// rather than the account -- it is the same for everyone, it is in the page
// source, and the overlay channel it opens is public.
const relayEndpoint = "wss://relay.tako.id:443/app/tako?protocol=7&client=mikkilens&version=8.4.0&flash=false"

// The alert the overlay invents when the dashboard's Test button is pressed.
// Taken from the overlay's own code: the test never reaches the server, so
// these are the only description of it there is.
const (
	exampleDonor     = "Tako"
	exampleAmount    = 10000
	exampleMessage   = "Disana gunung disini gunung, ditengah-tengahnya ada Contoh Pesan."
	exampleRecording = "/audio/sample-voice-note.mp3"
)

// errUnknownKey is the one failure worth saying out loud. It is a setting that
// is wrong and will stay wrong, as against a network that is briefly unhappy,
// and its symptom -- chat read straight over every donation -- looks exactly
// like this feature having never been switched on.
var errUnknownKey = errors.New(
	"Tako does not recognise this overlay key; check the alert overlay link in the dashboard")

// settings is the part of the overlay configuration that decides how long an
// alert stays up. Pointers, because the difference between "the creator set
// zero" and "the creator set nothing" is the difference between no pause and
// the default one.
type settings struct {
	Alert struct {
		Duration          *float64 `json:"duration"`
		VNMaximumDuration *float64 `json:"vnMaximumDuration"`
	} `json:"alert"`
}

// alert is the part of one overlay payload that decides how long chat stays
// quiet. The stickers, badges and effects Tako sends with it are for the
// browser source to draw and none of MikkiLens's business.
type alert struct {
	Meta *struct {
		ID          string `json:"id"`
		IsTest      bool   `json:"isTest"`
		IsBroadcast bool   `json:"isBroadcast"`
		Currency    string `json:"currency"`
	} `json:"$"`

	Sender struct {
		Name string `json:"name"`
	} `json:"sender"`
	Amount  float64 `json:"amount"`
	Message string  `json:"message"`

	// A voice note is the donor's own recording, played instead of the message
	// being read out.
	RecordingURL string `json:"recordingUrl"`

	Settings settings `json:"_overlaySettings"`
}

// envelope is how every overlay response is wrapped.
type envelope struct {
	StatusCode int             `json:"statusCode"`
	Result     json.RawMessage `json:"result"`
}

// fetch asks the overlay endpoint what is waiting to be shown.
//
// It is a read and only ever a read. The endpoint also takes a PUT saying
// which alerts have been played, and that is what moves Tako's queue on --
// sending it from here would take donations off the browser source that is
// actually showing them, so nothing in this package writes.
func fetch(ctx context.Context, client *http.Client, origin, key string) (alert, bool, error) {
	endpoint := origin + "/api/v1/overlay/" + key

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return alert{}, false, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Overlay-Key", key)
	request.Header.Set("X-Path", overlayPath)
	// Deliberately empty and deliberately present. It tells Tako which alerts
	// this client already knows about, and MikkiLens keeps none: it wants to
	// hear about whatever is on screen now, not to claim a place in the queue.
	request.Header.Set("X-Queued-Gift-Ids", "")
	request.Header.Set("Referer", origin+overlayPath+"?overlay_key="+key)
	request.Header.Set("User-Agent", browserAgent)

	response, err := client.Do(request)
	if err != nil {
		return alert{}, false, fmt.Errorf("could not reach the Tako overlay: %w", err)
	}
	defer response.Body.Close()

	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return alert{}, false, fmt.Errorf("could not read the Tako overlay reply: %w", err)
	}

	switch response.StatusCode {
	case http.StatusOK, statusNoAlert:
	case http.StatusNotFound:
		return alert{}, false, errUnknownKey
	default:
		return alert{}, false, fmt.Errorf(
			"the Tako overlay answered %s", response.Status)
	}

	var wrapper envelope
	if err := json.Unmarshal(body, &wrapper); err != nil {
		return alert{}, false, fmt.Errorf("could not understand the Tako overlay reply: %w", err)
	}

	var found alert
	if len(wrapper.Result) > 0 {
		if err := json.Unmarshal(wrapper.Result, &found); err != nil {
			return alert{}, false, fmt.Errorf("could not understand the Tako alert: %w", err)
		}
	}

	// A 206 carries the overlay configuration and no alert. It is still worth
	// returning, because that configuration is where the duration comes from.
	return found, response.StatusCode == http.StatusOK && found.Meta != nil, nil
}

// holdFor is how long the alert overlay will be busy with one donation.
//
// The overlay keeps an alert up for its configured duration, except that a
// donation with a message stays up until Tako has finished reading it aloud,
// and a voice note stays up until the recording ends or the creator's cap cuts
// it off. Only the first of those is a number anyone has; the other two are
// estimated, because the alternative is asking Tako for the donor's audio and
// MikkiLens has no business downloading that to decide when to stop talking.
func holdFor(found alert, tuning config.Tako) time.Duration {
	duration := defaultAlertDuration
	if configured := found.Settings.Alert.Duration; configured != nil && *configured > 0 {
		duration = seconds(*configured)
	}

	switch {
	case found.RecordingURL != "":
		// However long the recording runs, the overlay gives up on it at the
		// creator's cap, so the cap is the longest this can last.
		if cap := found.Settings.Alert.VNMaximumDuration; cap != nil && *cap > 0 {
			duration = max(duration, seconds(*cap))
		}
	case strings.TrimSpace(found.Message) != "" && tuning.TTSCharsPerSecond > 0:
		spoken := seconds(float64(len([]rune(found.Message))) / tuning.TTSCharsPerSecond)
		duration = max(duration, spoken)
	}

	duration += alertLeadIn + seconds(tuning.ExtraSeconds)

	// Chat that goes quiet for the rest of a stream because a number came back
	// wrong is the failure worth ruling out, so the ceiling is absolute.
	if ceiling := seconds(tuning.MaxHoldS); ceiling > 0 && duration > ceiling {
		return ceiling
	}
	return duration
}

func seconds(value float64) time.Duration {
	if value <= 0 {
		return 0
	}
	return time.Duration(value * float64(time.Second))
}
