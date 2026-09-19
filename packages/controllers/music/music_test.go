package music

import (
	"os"
	"strings"
	"testing"
)

// The fixtures are real answers from youtube.com's search, cut down to the
// fields that are read. Two languages, because the difference between them is
// not cosmetic: the Indonesian answer writes three minutes thirty-nine as
// "3.39", and reading that as a decimal is exactly the bug these tests exist to
// keep out.

func load(t *testing.T, name string) []byte {
	t.Helper()
	page, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("could not read the fixture: %v", err)
	}
	return page
}

func TestParseReadsTheIndonesianAnswer(t *testing.T) {
	songs := Parse(load(t, "youtube_id.json"))

	if len(songs) != Limit {
		t.Fatalf("got %d songs, want %d", len(songs), Limit)
	}
	first := songs[0]
	// "TULUS - Monokrom (Official Music Video)" on Tulus's own channel: the
	// label and the repeated artist are not part of what is read out.
	if first.Title != "Monokrom" || first.Artist != "Tulus" {
		t.Fatalf("first song = %q by %q", first.Title, first.Artist)
	}
	if first.Duration != "3.39" {
		t.Errorf("duration as written = %q, want 3.39", first.Duration)
	}
	if first.Minutes != 3 || first.Seconds != 39 {
		t.Errorf("running time = %d:%02d, want 3:39", first.Minutes, first.Seconds)
	}
	if first.VideoID != "QqJ-Vp8mvbk" {
		t.Errorf("video id = %q", first.VideoID)
	}
}

func TestParseReadsTheEnglishAnswer(t *testing.T) {
	songs := Parse(load(t, "youtube_en.json"))

	if len(songs) != Limit {
		t.Fatalf("got %d songs, want %d", len(songs), Limit)
	}
	first := songs[0]
	if first.Artist != "Daft Punk" || !strings.HasPrefix(first.Title, "Get Lucky") {
		t.Fatalf("first song = %q by %q", first.Title, first.Artist)
	}
	if strings.Contains(first.Title, "Official") {
		t.Errorf("title %q still carries its label", first.Title)
	}
	if first.Minutes != 4 || first.Seconds != 9 {
		t.Errorf("running time = %d:%02d, want 4:09", first.Minutes, first.Seconds)
	}
}

// A mix, a full album or an hour-long loop is music, and not the song she
// named. Both fixtures have several: "15.12", "58.11", "1.15.14", "18:08".
func TestOnlyASongsLengthIsOffered(t *testing.T) {
	for _, name := range []string{"youtube_en.json", "youtube_id.json"} {
		for index, song := range Parse(load(t, name)) {
			if length := song.length(); length < MinLength || length > MaxLength {
				t.Errorf("%s: result %d runs %d:%02d, outside one to ten minutes",
					name, index+1, song.Minutes, song.Seconds)
			}
		}
	}
}

// The order results come back in is the ranking, and the top result is the one
// she means far more often than not. Losing it to map iteration would be
// invisible: five plausible songs, in a different order every time.
func TestParseKeepsTheRanking(t *testing.T) {
	first := Parse(load(t, "youtube_id.json"))
	for attempt := 0; attempt < 20; attempt++ {
		again := Parse(load(t, "youtube_id.json"))
		for index := range first {
			if again[index].VideoID != first[index].VideoID {
				t.Fatalf("result %d changed between two parses of the same answer", index+1)
			}
		}
	}
}

// Music goes ahead of what is not, and only that: the same artist's other
// songs are music too and must not jump ahead of the one she asked for.
func TestMusicComesFirstButRelevanceIsKept(t *testing.T) {
	page := []byte(`{"contents":[` +
		video("game", "Get Lucky - Just Dance 2014 5 stars", "Gamer", "4:40", "") + `,` +
		video("asked", "Get Lucky (Official Audio)", "Daft Punk", "4:09", "BADGE_STYLE_TYPE_VERIFIED_ARTIST") + `,` +
		video("other", "Instant Crush (Official Video)", "Daft Punk", "5:40", "BADGE_STYLE_TYPE_VERIFIED_ARTIST") + `,` +
		video("lyric", "Get Lucky (Lyrics)", "7clouds", "4:06", "") + `]}`)

	var order []string
	for _, song := range Parse(page) {
		order = append(order, song.VideoID)
	}
	want := []string{"asked", "other", "lyric", "game"}
	if strings.Join(order, " ") != strings.Join(want, " ") {
		t.Errorf("order = %v, want %v", order, want)
	}
}

func video(id, title, channel, length, badge string) string {
	badges := ""
	if badge != "" {
		badges = `,"ownerBadges":[{"metadataBadgeRenderer":{"style":"` + badge + `"}}]`
	}
	return `{"videoRenderer":{"videoId":"` + id + `","title":{"runs":[{"text":"` + title + `"}]},` +
		`"ownerText":{"runs":[{"text":"` + channel + `"}]},"lengthText":{"simpleText":"` + length + `"}` +
		badges + `}}`
}

func TestSpeakableTitle(t *testing.T) {
	for _, test := range []struct {
		title, channel string
		own            bool
		want           string
	}{
		{"TULUS - Monokrom (Official Music Video)", "Tulus", true, "Monokrom"},
		{"Monokrom - Tulus | Lirik Lagu", "Indolirik", false, "Monokrom - Tulus"},
		// Somebody else's upload keeps the artist in the title: the channel
		// read out after it is not the artist.
		{"TULUS - Monokrom (Official Music Video Lyric)", "LIRIKIN OFFICIAL", false, "TULUS - Monokrom"},
		// A bracket that is part of the name stays.
		{"Get Lucky (feat. Pharrell Williams) [HD]", "Somebody", false, "Get Lucky (feat. Pharrell Williams)"},
		// Nothing left would be worse than the original.
		{"(Official Video)", "Somebody", false, "(Official Video)"},
	} {
		if got := speakableTitle(test.title, test.channel, test.own); got != test.want {
			t.Errorf("speakableTitle(%q) = %q, want %q", test.title, got, test.want)
		}
	}
}

func TestEveryResultCanBePlayed(t *testing.T) {
	for _, name := range []string{"youtube_en.json", "youtube_id.json"} {
		for index, song := range Parse(load(t, name)) {
			if song.VideoID == "" {
				t.Errorf("%s: result %d has nothing to play", name, index+1)
			}
			if song.Title == "" || song.Artist == "" {
				t.Errorf("%s: result %d has nothing to say", name, index+1)
			}
			if want := "https://www.youtube.com/watch?v=" + song.VideoID; song.URL() != want {
				t.Errorf("%s: url = %q, want %q", name, song.URL(), want)
			}
		}
	}
}

// Somebody else's markup, and it will change. When it does this has to come
// back empty rather than half-read: empty is a state the caller already says
// out loud.
func TestParseSurvivesRubbish(t *testing.T) {
	for _, page := range []string{
		"",
		"not json at all",
		"{}",
		`{"contents":null}`,
		`{"contents":{"videoRenderer":{}}}`,
		// No length: a live stream, which cannot be played from the start.
		`{"a":{"videoRenderer":{"videoId":"x","title":{"runs":[{"text":"Live now"}]}}}}`,
		// No id: nothing to play.
		`{"a":{"videoRenderer":{"title":{"runs":[{"text":"A song"}]},"lengthText":{"simpleText":"3:00"}}}}`,
	} {
		if songs := Parse([]byte(page)); len(songs) != 0 {
			t.Errorf("parsing %q gave %d songs, want none", page, len(songs))
		}
	}
}

func TestClockParts(t *testing.T) {
	for _, test := range []struct {
		duration string
		minutes  int
		seconds  int
	}{
		{"6:10", 6, 10},
		{"3.35", 3, 35},
		{"0:45", 0, 45},
		{"1:30:00", 90, 0}, // hours fold into minutes
		{"1.15.14", 75, 14},
		{"", 0, 0},
		{"208 jt x ditonton", 0, 0},
	} {
		minutes, seconds := clockParts(test.duration)
		if minutes != test.minutes || seconds != test.seconds {
			t.Errorf("clockParts(%q) = (%d, %d), want (%d, %d)",
				test.duration, minutes, seconds, test.minutes, test.seconds)
		}
	}
}

func TestSearchRefusesAnEmptyQuery(t *testing.T) {
	songs, err := Search(t.Context(), "   ", "id", "ID")
	if err == nil {
		t.Fatal("an empty query was accepted")
	}
	if songs != nil {
		t.Errorf("got %d songs back with an error", len(songs))
	}
}
