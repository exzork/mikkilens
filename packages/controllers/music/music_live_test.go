package music

import (
	"os"
	"testing"
)

// The real endpoint, behind MIKKILENS_LIVE=1 like the other live tests.
//
// The fixtures pin the parsing; this pins the request. They fail differently
// and for different reasons: a changed answer shape breaks the fixtures, and a
// changed client name, key or filter breaks only this -- silently, with a 200
// and an empty list, which is the failure worth having a test for.
func TestSearchAgainstYouTubeMusic(t *testing.T) {
	if os.Getenv("MIKKILENS_LIVE") != "1" {
		t.Skip("set MIKKILENS_LIVE=1 to search YouTube Music for real")
	}

	for _, test := range []struct{ query, language, region string }{
		{"tulus monokrom", "id", "ID"},
		{"daft punk get lucky", "en", "US"},
	} {
		songs, err := Search(t.Context(), test.query, test.language, test.region)
		if err != nil {
			t.Fatalf("%s: %v", test.query, err)
		}
		if len(songs) == 0 {
			t.Fatalf("%s: no results -- the client, the key or the filter has moved", test.query)
		}

		for index, song := range songs {
			t.Logf("%s: %d. %q by %q, %d:%02d (%s)",
				test.language, index+1, song.Title, song.Artist,
				song.Minutes, song.Seconds, song.VideoID)

			if song.VideoID == "" {
				t.Errorf("%s: result %d has nothing to play", test.query, index+1)
			}
			if song.Title == "" || song.Artist == "" {
				t.Errorf("%s: result %d has nothing to say", test.query, index+1)
			}
		}
		if len(songs) > Limit {
			t.Errorf("%s: %d results, want at most %d", test.query, len(songs), Limit)
		}
	}
}
