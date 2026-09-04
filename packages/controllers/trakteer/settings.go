package trakteer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/exzork/mikkilens/packages/controllers/pusher"
	"github.com/exzork/mikkilens/packages/core/config"
)

const (
	overlayOrigin = "https://stream.trakteer.id"
	apiOrigin     = "https://api.trakteer.id"

	// Trakteer's relay is ordinary Pusher on its own host. The application key
	// is in the overlay's page source and is the same for every creator: what
	// identifies the account is the channel, which is why the overlay link is
	// worth treating as a secret and this is not.
	relayEndpoint = "wss://socket.trakteer.id:443/app/2ae25d102cc6cd41100a" +
		"?protocol=7&client=mikkilens&version=8.4.0&flash=false"
)

// What the notification overlay falls back to when the creator has set
// nothing, taken from the overlay's own code so the quiet lasts as long as the
// alert does.
const (
	defaultDelay          = 5 * time.Second
	defaultVoiceNote      = 10 * time.Second
	defaultVoiceNoteMax   = 30 * time.Second
	defaultMediaShare     = 5 * time.Second
	defaultMediaShareMax  = 30 * time.Second
	defaultTTSMaxDuration = 60 * time.Second
)

// errUnknownKey is the one failure worth saying out loud: a setting that is
// wrong and will stay wrong, as against a network that is briefly unhappy.
var errUnknownKey = errors.New(
	"Trakteer does not recognise this stream key; check the notification overlay link in the dashboard")

// Link is the two things the overlay address carries.
//
// Trakteer needs both, and both come out of the one link she copies from the
// dashboard -- which is the only form of this anyone has, so it is the form
// the config takes rather than making her pick it apart by hand.
type Link struct {
	Key  string // trstream-...
	Hash string
}

// ParseLink pulls the key and hash out of a notification overlay address.
func ParseLink(link string) (Link, error) {
	trimmed := strings.TrimSpace(link)
	if trimmed == "" {
		return Link{}, errors.New("the Trakteer overlay link is empty")
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return Link{}, fmt.Errorf("could not understand the Trakteer overlay link: %w", err)
	}
	query := parsed.Query()
	found := Link{Key: query.Get("key"), Hash: query.Get("hash")}

	if found.Key == "" || found.Hash == "" {
		return Link{}, errors.New(
			"the Trakteer overlay link needs both key and hash in it; " +
				"copy the whole notification overlay address from the dashboard")
	}
	return found, nil
}

// Channels are the two the overlay listens on: the real one, and the one the
// dashboard's test button broadcasts to.
func (l Link) Channels() []string {
	return []string{
		"creator-stream." + l.Hash + "." + l.Key,
		"creator-stream-test." + l.Hash + "." + l.Key,
	}
}

// settings is the creator's notification overlay configuration.
//
// Every value arrives as a string, including the numbers and the booleans,
// which is why nothing here is typed as what it means.
type settings struct {
	Delay string `json:"nt_delay"`

	TTS            string `json:"nt_tts"`
	TTSMaxDuration string `json:"nt_tts_max_duration"`

	VoiceNote            string `json:"nt_voice_note"`
	VoiceNoteDuration    string `json:"nt_voice_note_duration"`
	VoiceNoteMaxDuration string `json:"nt_voice_note_max_duration"`
	CapVoiceNote         string `json:"nt_active_max_vn_duration"`

	MediaShare            string `json:"nt_media_share"`
	MediaShareDuration    string `json:"nt_media_share_duration"`
	MediaShareMaxDuration string `json:"nt_media_share_max_duration"`
	CapMediaShare         string `json:"nt_active_max_duration"`
}

// tip is one donation, as the overlay is told about it.
type tip struct {
	Type string `json:"type"`

	// The identifier is "id" on the wire. The overlay's own code reads
	// "tip_id" in one place, so both are taken: an id that is always empty
	// would leave every donation looking new, and the same one arriving on
	// both channels would be held for twice.
	ID    string `json:"id"`
	TipID string `json:"tip_id"`

	SupporterName    string  `json:"supporter_name"`
	SupporterMessage string  `json:"supporter_message"`
	Unit             string  `json:"unit"`
	Price            string  `json:"price"`
	Quantity         float64 `json:"quantity"`

	// Media is a voice note, a video, a gif, an image or a soundboard clip.
	// Which one it is decides how long the alert stays up, so only the key
	// matters here and not what it points at.
	Media map[string]json.RawMessage `json:"media"`
}

// isReal reports whether this is a donation rather than the dashboard's test.
func (t tip) isReal() bool {
	switch t.Type {
	case "new-tip-success", "new-tip-success-approved", "new-tip-replay":
		return true
	default:
		return false
	}
}

// isTip reports whether this is something the overlay would show at all.
func (t tip) isTip() bool { return t.isReal() || t.Type == "new-tip-simulation" }

// id is what identifies this donation, whichever spelling it arrived under.
func (t tip) id() string {
	if t.ID != "" {
		return t.ID
	}
	return t.TipID
}

func (t tip) has(kind string) bool {
	_, present := t.Media[kind]
	return present
}

// units is the quantity, floored at one. A donation of nothing is not a thing
// Trakteer sends, but a zero here would collapse every duration to nothing.
func (t tip) units() float64 {
	if t.Quantity < 1 {
		return 1
	}
	return t.Quantity
}

// fetchSettings reads the creator's overlay configuration.
//
// A read and only ever a read, as with the relay: nothing in this package
// tells Trakteer that anything has been shown.
func fetchSettings(ctx context.Context, client *http.Client, origin, key string) (settings, error) {
	endpoint := origin + "/v2/stream/" + url.PathEscape(key) + "/settings/nt"

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return settings{}, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Referer", overlayOrigin+"/")
	request.Header.Set("User-Agent", pusher.BrowserAgent)

	response, err := client.Do(request)
	if err != nil {
		return settings{}, fmt.Errorf("could not reach Trakteer: %w", err)
	}
	defer response.Body.Close()

	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return settings{}, fmt.Errorf("could not read the Trakteer reply: %w", err)
	}
	if response.StatusCode == http.StatusNotFound {
		return settings{}, errUnknownKey
	}
	if response.StatusCode != http.StatusOK {
		return settings{}, fmt.Errorf("Trakteer answered %s", response.Status)
	}

	// The settings come back as a list holding one entry.
	var wrapper struct {
		Settings []settings `json:"settings"`
	}
	if err := json.Unmarshal(body, &wrapper); err != nil {
		return settings{}, fmt.Errorf("could not understand the Trakteer settings: %w", err)
	}
	if len(wrapper.Settings) == 0 {
		return settings{}, errUnknownKey
	}
	return wrapper.Settings[0], nil
}

// holdFor is how long the notification overlay will be busy with one donation.
//
// The alert is up for the creator's delay, except that a voice note or a
// shared video runs for its own length instead -- and those scale with how
// many units were given, because Trakteer plays one per unit up to a cap.
func holdFor(given tip, set settings, tuning config.Trakteer) time.Duration {
	duration := durationOf(set.Delay, defaultDelay)

	if enabled(set.VoiceNote) && given.has("voice") {
		duration = max(duration, scaled(
			durationOf(set.VoiceNoteDuration, defaultVoiceNote), given.units(),
			enabled(set.CapVoiceNote), durationOf(set.VoiceNoteMaxDuration, defaultVoiceNoteMax)))
	}
	if enabled(set.MediaShare) && (given.has("video") || given.has("gif") || given.has("image")) {
		duration = max(duration, scaled(
			durationOf(set.MediaShareDuration, defaultMediaShare), given.units(),
			enabled(set.CapMediaShare), durationOf(set.MediaShareMaxDuration, defaultMediaShareMax)))
	}
	if enabled(set.TTS) && strings.TrimSpace(given.SupporterMessage) != "" && tuning.TTSCharsPerSecond > 0 {
		spoken := seconds(float64(len([]rune(given.SupporterMessage))) / tuning.TTSCharsPerSecond)
		// Trakteer stops reading at its own limit however long the message is.
		duration = max(duration, min(spoken, durationOf(set.TTSMaxDuration, defaultTTSMaxDuration)))
	}

	duration += seconds(tuning.ExtraSeconds)

	// Chat going quiet for the rest of a stream because a number came back
	// wrong is the failure worth ruling out, so the ceiling is absolute.
	if ceiling := seconds(tuning.MaxHoldS); ceiling > 0 && duration > ceiling {
		return ceiling
	}
	return duration
}

// scaled is one-per-unit up to the cap, which is what the overlay does with a
// donation of several units at once.
func scaled(each time.Duration, units float64, capped bool, ceiling time.Duration) time.Duration {
	total := time.Duration(float64(each) * units)
	if capped && total > ceiling {
		return ceiling
	}
	return total
}

// enabled reads one of Trakteer's string booleans.
func enabled(value string) bool { return strings.EqualFold(strings.TrimSpace(value), "true") }

// durationOf reads one of Trakteer's string seconds, falling back when it is
// missing or nonsense rather than collapsing the hold to nothing.
func durationOf(value string, fallback time.Duration) time.Duration {
	parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return seconds(parsed)
}

func seconds(value float64) time.Duration {
	if value <= 0 {
		return 0
	}
	return time.Duration(value * float64(time.Second))
}
