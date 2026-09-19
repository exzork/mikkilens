// Package music searches YouTube for something to play, so a song can be found
// by name and played without anybody looking at a screen.
//
// The whole of YouTube rather than YouTube Music's catalogue. The catalogue
// only has what a label put there, and a lot of what she asks for -- a cover, a
// live version, a song from a smaller artist, a soundtrack -- is only on
// YouTube proper. The price is that YouTube is mostly not music, so the results
// are sifted: only what runs between one and ten minutes, which is the length
// of a song rather than of a short or a two-hour mix, and what looks like music
// ahead of what does not.
//
// It talks to the same endpoint youtube.com's own search page calls, with the
// client name it identifies itself by. No key to obtain, no account to connect,
// no quota to spend. That last one is the reason it is not the YouTube Data
// API: search.list costs a hundred units of a ten thousand unit day, so twenty
// searches during a stream would be a fifth of the allowance that chat and the
// viewer count are also drawing on, and the failure would land on chat rather
// than on the search that caused it.
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
	"regexp"
	"sort"
	"strings"
	"time"
)

// Song is one result, in the parts worth reading aloud.
//
// Not the video's title as uploaded, because "TULUS - Monokrom (Official Music
// Video)" by Tulus read out is the artist twice and a label nobody needs to
// hear. The wording is the caller's, in her language; the parts are here.
type Song struct {
	Title  string `json:"title"`
	Artist string `json:"artist"`

	// Album is empty from a YouTube search, which does not say. Kept because
	// the window shows one when there is one.
	Album string `json:"album"`

	// Duration as YouTube writes it, which is not one thing: "6:10" in
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

// length is the running time in seconds.
func (s Song) length() int { return s.Minutes*60 + s.Seconds }

// URL is where the song plays. youtube.com, because that is where every result
// now comes from; plenty of them are not on YouTube Music at all.
func (s Song) URL() string {
	return "https://www.youtube.com/watch?v=" + url.QueryEscape(s.VideoID)
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

// MinLength and MaxLength bound what counts as a song, in seconds.
//
// Under a minute is a short, a clip or a trailer. Over ten is a mix, a full
// album, an hour-long loop or a concert -- all of which are real music and none
// of which is the one song she asked for by name. A live stream has no length
// at all and falls out here too, which is right: it cannot be played from the
// start.
const (
	MinLength = 60
	MaxLength = 10 * 60
)

// timeout is the whole lookup. She is standing there waiting for it, and past
// this she is better served by "I could not search for that" than by more
// silence.
const timeout = 12 * time.Second

// endpoint, clientName and clientVersion are how youtube.com's search page
// identifies itself. No key in the query string: the endpoint answers the same
// without one, and a string shaped exactly like a Google API key is a false
// alarm for every secret scanner that looks at this repository.
const (
	endpoint      = "https://www.youtube.com/youtubei/v1/search?prettyPrint=false"
	clientName    = "WEB"
	clientVersion = "2.20250101.00.00"
)

// videosOnly is the filter the search page sends for "Type: Video". Without it
// the first page is diluted with channels, playlists and shorts shelves, none
// of which is something to play.
//
// It is opaque -- a base64 protobuf of the same filter a person clicks -- so a
// day when it stops meaning "videos" shows up as fewer results rather than as
// an error, because everything that is not a video is skipped anyway.
const videosOnly = "EgIQAQ%3D%3D"

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
		"params": videosOnly,
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
	request.Header.Set("Origin", "https://www.youtube.com")
	request.Header.Set("Referer", "https://www.youtube.com/")
	// A browser's user agent, for the same reason [search] sends one: this is
	// the same public endpoint the search page calls, asked once, on her
	// behalf.
	request.Header.Set("User-Agent",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 "+
			"(KHTML, like Gecko) Chrome/125.0.0.0 Safari/537.36")

	response, err := client.Do(request)
	if err != nil {
		return nil, &Error{Reason: "could not reach YouTube: " + err.Error()}
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return nil, &Error{Reason: fmt.Sprintf("YouTube answered %s", response.Status)}
	}

	page, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return nil, &Error{Reason: err.Error()}
	}
	return Parse(page), nil
}

// Parse pulls the songs out of an answer: every video in it, only the ones a
// song's length, music ahead of the rest, and the first Limit of those.
//
// It hunts for the video renderers anywhere in the document rather than
// walking a fixed path to them. The path is deep, it is not ours, and it has
// moved before; the shape of one video -- an id, a title and a length -- is the
// part that has stayed still. Anything without those is skipped, so a document
// that has changed out from under this comes back empty rather than half-read.
//
// Exported so the tests can work from a saved answer without the network.
func Parse(page []byte) []Song {
	var document any
	if err := json.Unmarshal(page, &document); err != nil {
		return nil
	}

	var found []candidate
	seen := map[string]bool{}
	walk(document, func(video map[string]any) {
		song, music, ok := readVideo(video)
		if !ok || seen[song.VideoID] {
			return
		}
		seen[song.VideoID] = true
		if length := song.length(); length < MinLength || length > MaxLength {
			return
		}
		found = append(found, candidate{song: song, music: music})
	})

	// Music first, and otherwise YouTube's own order. Stable, because that
	// order is the relevance ranking and is worth more than anything this
	// could work out: an official upload of a different song by the same
	// artist is music too, and must not jump ahead of the song she named.
	sort.SliceStable(found, func(i, j int) bool { return found[i].music && !found[j].music })

	songs := make([]Song, 0, Limit)
	for _, entry := range found {
		if len(songs) == Limit {
			break
		}
		songs = append(songs, entry.song)
	}
	return songs
}

type candidate struct {
	song  Song
	music bool
}

// walk visits every videoRenderer in document order. Nothing inside one is
// another result, so it does not look further in.
func walk(node any, visit func(map[string]any)) {
	switch value := node.(type) {
	case map[string]any:
		if video, ok := value["videoRenderer"].(map[string]any); ok {
			visit(video)
			return
		}
		// Map iteration is unordered, and the order results come back in is
		// the ranking. Only one key at each level ever holds the results, so
		// this stays deterministic in practice; the ordering that matters is
		// within the section's own array, which is a slice and keeps its order.
		for _, child := range value {
			walk(child, visit)
		}
	case []any:
		for _, child := range value {
			walk(child, visit)
		}
	}
}

// readVideo turns one video renderer into a song, reporting whether it looks
// like music, or that it is not something to offer at all.
func readVideo(video map[string]any) (Song, bool, bool) {
	videoID, _ := video["videoId"].(string)
	title := runsText(video["title"])
	if videoID == "" || title == "" {
		return Song{}, false, false
	}

	channel := runsText(video["ownerText"])
	duration, _ := dig(video, "lengthText", "simpleText").(string)
	duration = strings.TrimSpace(duration)

	artistChannel := hasBadge(video["ownerBadges"], "BADGE_STYLE_TYPE_VERIFIED_ARTIST")
	topic := strings.HasSuffix(channel, " - Topic")
	channel = strings.TrimSuffix(channel, " - Topic")

	song := Song{
		Title:    speakableTitle(title, channel, artistChannel || topic),
		Artist:   channel,
		Duration: duration,
		VideoID:  videoID,
	}
	song.Minutes, song.Seconds = clockParts(duration)
	music := artistChannel || topic || looksLikeMusic(title)
	return song, music, true
}

// musicWords are what a music upload says about itself in its title, in the
// two languages she searches in. Whole words only, so "mv" does not match
// inside something else.
var musicWords = regexp.MustCompile(`(?i)\b(official|lyrics?|lirik|audio|music|musik|mv|m/v|` +
	`video ?clip|video ?klip|cover|karaoke|remix|acoustic|akustik|instrumental|` +
	`feat\.?|ft\.|ost|lagu|song|live session|unplugged)\b`)

// looksLikeMusic reports whether a title reads as a song rather than a vlog, a
// game or a news clip.
func looksLikeMusic(title string) bool { return musicWords.MatchString(title) }

// labelWords mark a bracketed part of a title as a label rather than part of
// the song's name: "(Official Music Video)", "[Lirik]", "(HD)".
var labelWords = regexp.MustCompile(`(?i)\b(official|video|audio|lyrics?|lirik|mv|m/v|` +
	`hd|hq|4k|visuali[sz]er|clip|klip|remaster(ed)?)\b`)

var brackets = regexp.MustCompile(`\s*(\([^)]*\)|\[[^\]]*\]|【[^】]*】)`)

// speakableTitle is the title as it should be read out.
//
// Labels in brackets go, because "Official Music Video" is not part of the
// song's name. What follows a "|" goes, because that is where uploaders put
// their channel's tagline. And when the channel is the artist's own, a leading
// "Artist - " goes, because the artist is read out separately and hearing it
// twice is noise. Anything that would leave nothing keeps the original.
func speakableTitle(title, channel string, ownChannel bool) string {
	cleaned := brackets.ReplaceAllStringFunc(title, func(part string) string {
		if labelWords.MatchString(part) {
			return ""
		}
		return part
	})
	if cut := strings.Index(cleaned, " | "); cut > 0 {
		cleaned = cleaned[:cut]
	}
	if ownChannel && channel != "" {
		for _, dash := range []string{" - ", " – ", " — "} {
			prefix := channel + dash
			if len(cleaned) > len(prefix) && strings.EqualFold(cleaned[:len(prefix)], prefix) {
				cleaned = cleaned[len(prefix):]
				break
			}
		}
	}
	cleaned = strings.Join(strings.Fields(cleaned), " ")
	if cleaned == "" {
		return strings.TrimSpace(title)
	}
	return cleaned
}

// runsText joins a text object's runs, or reads its simpleText.
func runsText(node any) string {
	if simple, ok := dig(node, "simpleText").(string); ok {
		return strings.TrimSpace(simple)
	}
	runs, _ := dig(node, "runs").([]any)
	var builder strings.Builder
	for _, entry := range runs {
		run, _ := entry.(map[string]any)
		text, _ := run["text"].(string)
		builder.WriteString(text)
	}
	return strings.TrimSpace(builder.String())
}

// hasBadge reports whether a list of badges includes one style.
func hasBadge(node any, style string) bool {
	badges, _ := node.([]any)
	for _, badge := range badges {
		if found, _ := dig(badge, "metadataBadgeRenderer", "style").(string); found == style {
			return true
		}
	}
	return false
}

// isDuration reports whether a part reads as a running time.
//
// The separator is either a colon or a full stop, because YouTube writes the
// same six minutes ten as "6:10" in English and "6.10" in Indonesian. That is
// not a detail: the Indonesian form is also how a decimal number is written,
// so a duration taken at face value and handed to a voice comes out as "six
// point one zero".
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
