// Package music searches YouTube Music, so a song can be found by name and
// played without anybody looking at a screen.
//
// It talks to the same endpoint music.youtube.com talks to -- the one the web
// player itself calls, with the client name it identifies itself by. No key to
// obtain, no account to connect, no quota to spend. That last one is the
// reason it is not the YouTube Data API: search.list costs a hundred units of
// a ten thousand unit day, so twenty searches during a stream would be a fifth
// of the allowance that chat and the viewer count are also drawing on, and the
// failure would land on chat rather than on the search that caused it.
//
// The trade is the same one [search] makes: this is a shape observed rather
// than a shape promised, and it will change one day. So it is written to come
// back with nothing rather than with nonsense, and nothing is a state the
// caller already says out loud.
package music

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Song is one result, in the parts worth reading aloud.
//
// Not a title string, because "Get Lucky by Daft Punk, six minutes ten" and
// "Get Lucky (feat. Pharrell Williams and Nile Rodgers) • Daft Punk, Pharrell
// Williams & Nile Rodgers • Random Access Memories • 6:10" are the same
// result and only one of them is a sentence. The wording is the caller's, in
// her language; the parts are here.
type Song struct {
	Title  string `json:"title"`
	Artist string `json:"artist"`
	Album  string `json:"album"`

	// Duration as YouTube Music writes it, which is not one thing: "6:10" in
	// English and "6.10" in Indonesian. Kept for the window to show, and
	// emphatically not for reading aloud -- a voice handed "6.10" in
	// Indonesian says "six point one zero".
	Duration string `json:"duration"`

	// Minutes and Seconds are the same running time, taken apart so it can be
	// said as a running time in whatever language she has chosen.
	// Both zero means the answer did not carry one.
	Minutes int `json:"minutes"`
	Seconds int `json:"seconds"`

	VideoID string `json:"video_id"`
}

// HasDuration reports whether this result came with a running time. Some do
// not, and "nol menit" is a worse thing to hear than nothing at all.
func (s Song) HasDuration() bool { return s.Minutes > 0 || s.Seconds > 0 }

// URL is where the song plays.
//
// music.youtube.com rather than youtube.com: it opens in the player she
// already has a subscription and a listening history in, and it does not put
// a video on screen for a machine that is busy encoding one.
func (s Song) URL() string {
	return "https://music.youtube.com/watch?v=" + url.QueryEscape(s.VideoID)
}

// Error is a search that did not work, phrased for reading aloud.
type Error struct{ Reason string }

func (e *Error) Error() string { return e.Reason }

// Limit is how many results are carried back.
//
// Five, because five is what a person can hold in their head while they are
// being read to. She hears them one at a time and then says a number, and a
// list long enough that the first one has gone by the time the last is read is
// a list she has to ask for again.
const Limit = 5

// timeout is the whole lookup. She is standing there waiting for it, and past
// this she is better served by "I could not search for that" than by more
// silence.
const timeout = 12 * time.Second

// endpoint, clientName and clientVersion are how the web player identifies
// itself.
//
// No key in the query string, deliberately. The web player sends one -- the
// public InnerTube key it ships in its own page source, which identifies the
// client and grants nothing -- and the endpoint answers the same either way,
// so carrying it bought nothing and cost something: it is the exact shape of a
// Google API key, so every secret scanner that ever looks at this repository
// reports a credential, and whoever reads the alert has to work out that it is
// not one. A false alarm that has to be re-dismissed for ever is worse than no
// alarm, because it is how a real one gets waved through.
const (
	endpoint      = "https://music.youtube.com/youtubei/v1/search?prettyPrint=false"
	clientName    = "WEB_REMIX"
	clientVersion = "1.20240101.01.00"
)

// songsOnly is the filter the web player sends when the "Songs" tab is
// chosen. Without it the answer is a mixture of songs, videos, albums,
// artists and playlists, and four of those five are not something to play.
//
// It is opaque -- a base64 protobuf of the same filter a person clicks -- so
// it is here as a constant rather than built, and a day when it stops meaning
// "songs" shows up as results that are not songs rather than as an error.
const songsOnly = "EgWKAQIIAWoKEAoQAxAEEAkQBQ%3D%3D"

var client = &http.Client{Timeout: timeout}

// Search finds songs matching a query, best first.
//
// The language and region are passed through because they change what comes
// back: the same query answers differently in Indonesia than in the United
// States, and hers is the one that should decide.
func Search(ctx context.Context, query, language, region string) ([]Song, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, &Error{Reason: "there was nothing to search for"}
	}

	timed, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	body, err := json.Marshal(map[string]any{
		"context": map[string]any{
			"client": map[string]any{
				"clientName":    clientName,
				"clientVersion": clientVersion,
				"hl":            orDefault(language, "en"),
				"gl":            orDefault(strings.ToUpper(region), "US"),
			},
		},
		"query":  query,
		"params": songsOnly,
	})
	if err != nil {
		return nil, &Error{Reason: err.Error()}
	}

	request, err := http.NewRequestWithContext(
		timed, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, &Error{Reason: err.Error()}
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://music.youtube.com")
	request.Header.Set("Referer", "https://music.youtube.com/")
	// A browser's user agent, for the same reason [search] sends one: this is
	// the same public endpoint the web player calls, asked once, on her behalf.
	request.Header.Set("User-Agent",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 "+
			"(KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")

	response, err := client.Do(request)
	if err != nil {
		return nil, &Error{Reason: "could not reach YouTube Music: " + err.Error()}
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return nil, &Error{Reason: fmt.Sprintf("YouTube Music answered %s", response.Status)}
	}

	page, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return nil, &Error{Reason: err.Error()}
	}
	return Parse(page), nil
}

// Parse pulls the songs out of an answer.
//
// It hunts for the item renderers anywhere in the document rather than walking
// a fixed path to them. The path is deep, it is not ours, and it has moved
// before; the shape of one item -- a video id and some columns of text -- is
// the part that has stayed still. Anything that does not have both is skipped,
// so a document that has changed out from under this comes back empty rather
// than half-read.
//
// Exported so the tests can work from a saved answer without the network.
func Parse(page []byte) []Song {
	var document any
	if err := json.Unmarshal(page, &document); err != nil {
		return nil
	}

	songs := make([]Song, 0, Limit)
	seen := map[string]bool{}
	walk(document, func(item map[string]any) bool {
		song, ok := readItem(item)
		if !ok || seen[song.VideoID] {
			return true
		}
		seen[song.VideoID] = true
		songs = append(songs, song)
		return len(songs) < Limit
	})
	return songs
}

// walk visits every musicResponsiveListItemRenderer in document order, and
// stops as soon as the visitor says it has enough.
func walk(node any, visit func(map[string]any) bool) bool {
	switch value := node.(type) {
	case map[string]any:
		if item, ok := value["musicResponsiveListItemRenderer"].(map[string]any); ok {
			return visit(item)
		}
		// Map iteration is unordered, and the order results come back in is
		// the ranking. Only one key at each level ever holds the results, so
		// this stays deterministic in practice; the ordering that matters is
		// within the shelf's own array, which is a slice and keeps its order.
		for _, child := range value {
			if !walk(child, visit) {
				return false
			}
		}
	case []any:
		for _, child := range value {
			if !walk(child, visit) {
				return false
			}
		}
	}
	return true
}

// readItem turns one item renderer into a song, or reports that it is not one.
func readItem(item map[string]any) (Song, bool) {
	videoID, _ := dig(item, "playlistItemData", "videoId").(string)
	if videoID == "" {
		// Albums, artists and playlists come back in the same renderer and
		// have no video to play. Skipping them here is why a filter that
		// stopped filtering degrades into fewer results rather than into
		// results that do nothing when chosen.
		return Song{}, false
	}

	columns, _ := item["flexColumns"].([]any)
	if len(columns) == 0 {
		return Song{}, false
	}
	title := columnText(columns, 0)
	if title == "" {
		return Song{}, false
	}

	song := Song{Title: title, VideoID: videoID}
	song.Artist, song.Album, song.Duration = splitByline(columnText(columns, 1))
	song.Minutes, song.Seconds = clockParts(song.Duration)
	return song, true
}

// columnText joins the runs of one flex column into its text.
func columnText(columns []any, index int) string {
	if index >= len(columns) {
		return ""
	}
	column, _ := columns[index].(map[string]any)
	runs, _ := dig(column, "musicResponsiveListItemFlexColumnRenderer", "text", "runs").([]any)

	var builder strings.Builder
	for _, entry := range runs {
		run, _ := entry.(map[string]any)
		text, _ := run["text"].(string)
		builder.WriteString(text)
	}
	return strings.TrimSpace(builder.String())
}

// splitByline reads the second column, which is one line with bullets in it:
//
//	Daft Punk, Pharrell Williams & Nile Rodgers • Random Access Memories • 6:10
//
// The album is missing on a single, and on some results the plays count sits
// where the album does. So the ends are read rather than the positions: a
// trailing "6:10" is the duration and the first part is always the artist,
// and whatever is in between is the album only when there is exactly one
// thing there.
func splitByline(line string) (artist, album, duration string) {
	if line == "" {
		return "", "", ""
	}
	parts := []string{}
	for _, part := range strings.Split(line, "•") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	if len(parts) == 0 {
		return "", "", ""
	}

	if last := parts[len(parts)-1]; isDuration(last) {
		duration = last
		parts = parts[:len(parts)-1]
	}
	if len(parts) > 0 {
		artist = parts[0]
		parts = parts[1:]
	}
	if len(parts) == 1 {
		album = parts[0]
	}
	return artist, album, duration
}

// isDuration reports whether a part reads as a running time.
//
// The separator is either a colon or a full stop, because YouTube Music writes
// the same six minutes ten as "6:10" in English and "6.10" in Indonesian. That
// is not a detail: the Indonesian form is also how a decimal number is
// written, so a duration taken at face value and handed to a voice comes out
// as "six point one zero".
func isDuration(part string) bool {
	digits, separators := 0, 0
	for _, character := range part {
		switch {
		case character >= '0' && character <= '9':
			digits++
		case character == ':' || character == '.':
			separators++
		default:
			return false
		}
	}
	return digits > 0 && separators > 0
}

// clockParts takes a running time apart, so it can be said rather than spelled
// out. Anything it cannot read comes back as zeroes, which the caller says as
// a song with no running time rather than as a song of no length.
func clockParts(duration string) (minutes, seconds int) {
	if !isDuration(duration) {
		return 0, 0
	}
	fields := strings.FieldsFunc(duration, func(r rune) bool { return r == ':' || r == '.' })
	// Hours are folded into minutes. A ninety minute mix read as "one hour
	// thirty" needs a second sentence shape for a case she will meet about
	// once; "ninety minutes" is the same fact in the shape already there.
	total := 0
	for _, field := range fields {
		value := 0
		for _, character := range field {
			value = value*10 + int(character-'0')
		}
		total = total*60 + value
	}
	return total / 60, total % 60
}

// dig walks a chain of map keys, returning nil the moment one is missing.
func dig(node any, keys ...string) any {
	for _, key := range keys {
		object, ok := node.(map[string]any)
		if !ok {
			return nil
		}
		node = object[key]
	}
	return node
}

func orDefault(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
