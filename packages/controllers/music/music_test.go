package music

import (
	"os"
	"testing"
)

// The fixtures are real answers from music.youtube.com, with the tracking
// parameters and the thumbnails taken out. Two languages, because the
// difference between them is not cosmetic: the Indonesian answer writes six
// minutes ten as "6.10", and reading that as a decimal is exactly the bug
// these tests exist to keep out.

func load(t *testing.T, name string) []byte {
	t.Helper()
	page, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("could not read the fixture: %v", err)
	}
	return page
}

func TestParseReadsTheEnglishAnswer(t *testing.T) {
	songs := Parse(load(t, "search_en.json"))

	if len(songs) != Limit {
		t.Fatalf("got %d songs, want %d", len(songs), Limit)
	}
	first := songs[0]
	if first.Title != "Get Lucky (feat. Pharrell Williams and Nile Rodgers)" {
		t.Errorf("title = %q", first.Title)
	}
	if first.Artist != "Daft Punk, Pharrell Williams & Nile Rodgers" {
		t.Errorf("artist = %q", first.Artist)
	}
	if first.Album != "Random Access Memories" {
		t.Errorf("album = %q", first.Album)
	}
	if first.Minutes != 6 || first.Seconds != 10 {
		t.Errorf("running time = %d:%02d, want 6:10", first.Minutes, first.Seconds)
	}
	if first.VideoID != "4D7u5KF7SP8" {
		t.Errorf("video id = %q", first.VideoID)
	}
}

// The Indonesian answer separates minutes from seconds with a full stop. Read
// as written it is a decimal, and a voice says "tiga koma tiga lima" for a
// song that is three minutes thirty-five.
func TestParseReadsAnIndonesianRunningTime(t *testing.T) {
	songs := Parse(load(t, "search_id.json"))

	if len(songs) != Limit {
		t.Fatalf("got %d songs, want %d", len(songs), Limit)
	}
	first := songs[0]
	if first.Title != "Monokrom" || first.Artist != "Tulus" {
		t.Fatalf("first song = %q by %q", first.Title, first.Artist)
	}
	if first.Duration != "3.35" {
		t.Errorf("duration as written = %q, want 3.35", first.Duration)
	}
	if first.Minutes != 3 || first.Seconds != 35 {
		t.Errorf("running time = %d:%02d, want 3:35", first.Minutes, first.Seconds)
	}
	if !first.HasDuration() {
		t.Error("a song three and a half minutes long reports no running time")
	}
}

// The order results come back in is the ranking, and the top result is the one
// she means far more often than not. Losing it to map iteration would be
// invisible: five plausible songs, in a different order every time.
func TestParseKeepsTheRanking(t *testing.T) {
	want := []string{"Monokrom", "Hati-Hati di Jalan", "Manusia Kuat", "Pamit", "Langit Abu-Abu"}

	for attempt := 0; attempt < 20; attempt++ {
		songs := Parse(load(t, "search_id.json"))
		for index, title := range want {
			if songs[index].Title != title {
				t.Fatalf("result %d = %q, want %q", index+1, songs[index].Title, title)
			}
		}
	}
}

func TestEveryResultCanBePlayed(t *testing.T) {
	for _, name := range []string{"search_en.json", "search_id.json"} {
		for index, song := range Parse(load(t, name)) {
			if song.VideoID == "" {
				t.Errorf("%s: result %d has nothing to play", name, index+1)
			}
			if song.Title == "" {
				t.Errorf("%s: result %d has nothing to say", name, index+1)
			}
			if want := "https://music.youtube.com/watch?v=" + song.VideoID; song.URL() != want {
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
		`{"contents":{"musicResponsiveListItemRenderer":{}}}`,
		`{"contents":[{"musicResponsiveListItemRenderer":{"flexColumns":[]}}]}`,
		// An item with columns but no video: an album or an artist, which is
		// not something to play.
		`{"a":{"musicResponsiveListItemRenderer":{"flexColumns":[{"musicResponsiveListItemFlexColumnRenderer":{"text":{"runs":[{"text":"Random Access Memories"}]}}}]}}}`,
	} {
		if songs := Parse([]byte(page)); len(songs) != 0 {
			t.Errorf("parsing %q gave %d songs, want none", page, len(songs))
		}
	}
}

func TestSplitByline(t *testing.T) {
	for _, test := range []struct {
		line                    string
		artist, album, duration string
	}{
		{"Tulus • Monokrom • 3.35", "Tulus", "Monokrom", "3.35"},
		{"Daft Punk • Random Access Memories • 6:10", "Daft Punk", "Random Access Memories", "6:10"},
		// A single: no album between the artist and the running time.
		{"Hindia • 4:02", "Hindia", "", "4:02"},
		// Sometimes there is no running time at all.
		{"Hindia • Menari Dengan Bayangan", "Hindia", "Menari Dengan Bayangan", ""},
		{"Hindia", "Hindia", "", ""},
		{"", "", "", ""},
	} {
		artist, album, duration := splitByline(test.line)
		if artist != test.artist || album != test.album || duration != test.duration {
			t.Errorf("splitByline(%q) = (%q, %q, %q), want (%q, %q, %q)",
				test.line, artist, album, duration, test.artist, test.album, test.duration)
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
		{"", 0, 0},
		{"208 jt pemutaran", 0, 0},
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
