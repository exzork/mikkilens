package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/exzork/mikkilens/packages/controllers/music"
	"github.com/exzork/mikkilens/packages/core/i18n"
	"github.com/exzork/mikkilens/packages/core/intent"
)

// Finding a song by typing its name, and picking one of the five read back.
//
// The part with teeth is the picking. She hears "one" through "five" and
// answers with one of them, and what actually arrives is whatever the speech
// model made of it -- a digit on one machine, a word on another, a word inside
// a longer phrase on a third.

func withLocale(language string) *Engine {
	return &Engine{locale: i18n.Load(language)}
}

func TestANumberCanBeSaidAsADigitOrAsAWord(t *testing.T) {
	indonesian := withLocale("id")
	for spoken, want := range map[string]int{
		"2":          2,
		"dua":        2,
		"nomor dua":  2,
		"yang kedua": 0, // "kedua" is not "dua"; better refused than guessed at
		"satu":       1,
		"lima":       5,
		"5":          5,
	} {
		number, ok := indonesian.spokenNumber(spoken)
		if want == 0 {
			if ok {
				t.Errorf("%q was read as number %d, want refused", spoken, number)
			}
			continue
		}
		if !ok || number != want {
			t.Errorf("%q read as (%d, %v), want %d", spoken, number, ok, want)
		}
	}

	english := withLocale("en")
	for spoken, want := range map[string]int{
		"three":        3,
		"number three": 3,
		"1":            1,
	} {
		if number, ok := english.spokenNumber(spoken); !ok || number != want {
			t.Errorf("%q read as (%d, %v), want %d", spoken, number, ok, want)
		}
	}
}

// A number word inside a longer word is not a number. "Duaratus" is two
// hundred, and starting a song because a title contained it would be the kind
// of surprise she cannot see coming.
func TestANumberWordMustStandOnItsOwn(t *testing.T) {
	engine := withLocale("id")
	for _, spoken := range []string{"duaratus", "limapuluh", "satuan"} {
		if number, ok := engine.spokenNumber(spoken); ok {
			t.Errorf("%q was read as number %d", spoken, number)
		}
	}
}

// Nothing recognisable has to be refused rather than turned into a one. The
// answer to "I did not catch which number" is asking again, not playing
// something.
func TestAnUnreadableNumberIsRefused(t *testing.T) {
	engine := withLocale("id")
	for _, spoken := range []string{"", "   ", "monokrom", "0", "9", "12"} {
		if number, ok := engine.spokenNumber(spoken); ok {
			t.Errorf("%q was read as number %d, want refused", spoken, number)
		}
	}
}

// -- what is pickable ---------------------------------------------------------

func TestResultsAreForgottenAfterAWhile(t *testing.T) {
	var kept results
	kept.remember([]music.Song{{Title: "Monokrom", VideoID: "1RrF6Ee_io0"}})

	if songs, ok := kept.current(); !ok || len(songs) != 1 {
		t.Fatalf("a fresh search is not pickable: %v %v", songs, ok)
	}

	// "Play number two" an hour after a search is far more likely to be about
	// a search she has forgotten than about that one.
	kept.found = time.Now().Add(-resultsKeptFor - time.Minute)
	if songs, ok := kept.current(); ok {
		t.Errorf("results from %v ago are still pickable: %v", resultsKeptFor, songs)
	}
}

func TestNothingIsPickableBeforeASearch(t *testing.T) {
	var kept results
	if songs, ok := kept.current(); ok || songs != nil {
		t.Errorf("something was pickable before any search: %v", songs)
	}
}

// The caller gets a copy. A handler holding the slice while the next search
// replaces it would otherwise be reading a list that is being rewritten under
// it.
func TestPickableResultsAreACopy(t *testing.T) {
	var kept results
	kept.remember([]music.Song{{Title: "Monokrom"}})

	songs, _ := kept.current()
	songs[0].Title = "something else"

	again, _ := kept.current()
	if again[0].Title != "Monokrom" {
		t.Errorf("the remembered results were changed from outside: %q", again[0].Title)
	}
}

// -- asking for the typing box ------------------------------------------------

// The engine has no window of its own. What "putar lagu" does depends on
// whether the desktop app is there to open one, and being told to press a key
// that does nothing is worse than being told to say the song name instead.

func TestNothingIsWaitingBeforeTheWindowConnects(t *testing.T) {
	var box typing
	if box.request() {
		t.Error("a request was reported as opening a window with nothing waiting")
	}
}

func TestAWaitingWindowIsWokenByARequest(t *testing.T) {
	var box typing

	opened := make(chan int, 1)
	go func() { opened <- box.wait(t.Context(), 0) }()

	// Wait until the window is actually parked, so this tests the wake rather
	// than the race to get there.
	eventuallyWaiting(t, &box, 1)
	if !box.request() {
		t.Error("a request with a window waiting was reported as having none")
	}

	select {
	case count := <-opened:
		if count != 1 {
			t.Errorf("the window was woken with count %d, want 1", count)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the waiting window was never woken")
	}
}

// A request that arrived while the window was reconnecting must not be lost:
// she pressed the key, and something has to open.
func TestARequestMadeWhileNobodyWaitedIsStillAnswered(t *testing.T) {
	var box typing
	box.request()

	answered := make(chan int, 1)
	go func() { answered <- box.wait(t.Context(), 0) }()

	select {
	case count := <-answered:
		if count != 1 {
			t.Errorf("answered with count %d, want 1", count)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a request made before the window connected was lost")
	}
}

// A long poll comes back every half a minute and is asked again straight away.
// A request landing in that gap must not be told there is no window and then
// open one anyway a moment later, which is both answers at once.
func TestAWindowBetweenPollsStillCounts(t *testing.T) {
	var box typing
	box.init()
	box.seen = time.Now()

	if !box.request() {
		t.Error("a window between two polls was reported as gone")
	}

	// And one that really has gone is reported as gone, or she is told to press
	// a key that opens nothing.
	box.seen = time.Now().Add(-windowGrace - time.Minute)
	if box.request() {
		t.Error("a window closed a while ago was reported as still there")
	}
}

// A window whose request is cancelled -- the app quitting, the connection
// dropping -- must not leave a goroutine parked on the condition for ever.
func TestACancelledWaitLetsGo(t *testing.T) {
	var box typing

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan int, 1)
	go func() { done <- box.wait(ctx, 0) }()

	eventuallyWaiting(t, &box, 1)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a cancelled wait never returned")
	}

	box.mu.Lock()
	waiting := box.waiting
	box.mu.Unlock()
	if waiting != 0 {
		t.Errorf("%d waiters left behind", waiting)
	}
}

func eventuallyWaiting(t *testing.T, box *typing, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		box.mu.Lock()
		waiting := box.waiting
		box.mu.Unlock()
		if waiting == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d waiters", want)
}

// -- how a result sounds ------------------------------------------------------

// The running time is the reason these are said rather than relayed: YouTube
// Music writes six minutes ten as "6.10" in Indonesian, and a voice handed
// that says "enam koma satu nol".
func TestAResultIsSaidWithItsRunningTimeInWords(t *testing.T) {
	song := music.Song{
		Title: "Monokrom", Artist: "Tulus", Duration: "3.35", Minutes: 3, Seconds: 35,
	}

	spoken := withLocale("id").describe(1, song)
	if !strings.Contains(spoken, "3 menit 35") {
		t.Errorf("Indonesian said %q, want the running time in minutes", spoken)
	}
	if strings.Contains(spoken, "3.35") {
		t.Errorf("Indonesian relayed the written duration: %q", spoken)
	}

	spoken = withLocale("en").describe(1, song)
	if !strings.Contains(spoken, "3 minutes 35") {
		t.Errorf("English said %q, want the running time in minutes", spoken)
	}
}

// Some results come back without one, and "zero minutes" is a worse thing to
// hear than nothing at all.
func TestAResultWithNoRunningTimeSaysNone(t *testing.T) {
	song := music.Song{Title: "Monokrom", Artist: "Tulus"}

	for _, language := range []string{"id", "en"} {
		spoken := withLocale(language).describe(2, song)
		if strings.Contains(spoken, "0") && !strings.Contains(spoken, "2") {
			t.Errorf("%s said %q", language, spoken)
		}
		if !strings.Contains(spoken, "Monokrom") || !strings.Contains(spoken, "Tulus") {
			t.Errorf("%s said %q, want the song and the artist", language, spoken)
		}
	}
}

// Every result is numbered, because the number is how she picks one.
func TestEveryResultIsNumbered(t *testing.T) {
	engine := withLocale("id")
	for number := 1; number <= music.Limit; number++ {
		spoken := engine.describe(number, music.Song{Title: "a song", Artist: "an artist"})
		if !strings.HasPrefix(strings.TrimSpace(spoken), itoa(number)) {
			t.Errorf("result %d was said as %q", number, spoken)
		}
	}
}

func itoa(number int) string { return string(rune('0' + number)) }

// -- saying a key out loud ----------------------------------------------------

// "<ctrl>+<alt>+<f>" read out is "less than c t r l greater than plus less than
// a l t greater than", which is not a key anybody can find. This is the one
// place a config value is spoken rather than shown, so it has to be turned
// into words first.
func TestAKeyIsSaidAsWords(t *testing.T) {
	for combination, want := range map[string]string{
		"<ctrl>+<alt>+<f>":     "Control Alt F",
		"<ctrl>+<alt>+<m>":     "Control Alt M",
		"<ctrl>+<alt>+<space>": "Control Alt Space",
		"ctrl+alt+f":           "Control Alt F",
		"<shift>+<f13>":        "Shift F13",
		"<win>+<up>":           "Windows Up",
		"<ctrl>+<page_up>":     "Control Page Up",
		"<num_5>":              "Num 5",
		"":                     "",
	} {
		if got := spokenCombination(combination); got != want {
			t.Errorf("spokenCombination(%q) = %q, want %q", combination, got, want)
		}
	}
}

// Nothing that comes out of here may still look like a config file. A stray
// bracket or plus sign is read out character by character.
func TestASpokenKeyCarriesNoPunctuation(t *testing.T) {
	for _, combination := range []string{
		"<ctrl>+<alt>+<f>", "<ctrl>+<shift>+<num_multiply>", "<alt_gr>+<print_screen>",
	} {
		spoken := spokenCombination(combination)
		if strings.ContainsAny(spoken, "<>+_") {
			t.Errorf("spokenCombination(%q) = %q, which still has punctuation in it",
				combination, spoken)
		}
	}
}

// -- the rest -----------------------------------------------------------------

// The region changes what comes back, and "EN" is not a country. An unmapped
// language has to fall back to a real one rather than sending a language code
// where a region belongs.
func TestTheRegionIsAlwaysACountry(t *testing.T) {
	for language, want := range map[string]string{
		"id": "ID", "ID": "ID", " id ": "ID",
		"en": "US", "": "US", "ja": "US",
	} {
		if got := regionFor(language); got != want {
			t.Errorf("regionFor(%q) = %q, want %q", language, got, want)
		}
	}
}

// A command that exists in code and in no command file is a command she cannot
// say. Both shipped files have to carry all three.
func TestBothCommandFilesCarryTheMusicCommands(t *testing.T) {
	for _, path := range []string{"../../commands.id.toml", "../../commands.en.toml"} {
		set, err := intent.SetFromFile(path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		for _, id := range []string{"search_music", "play_song", "list_songs", "chat_mute", "chat_unmute"} {
			if !set.Has(id) {
				t.Errorf("%s has no %s command", path, id)
			}
		}
	}
}

// Searching by voice has to be able to carry the song name, or the spoken path
// is only ever a way of being told to type it.
func TestSearchingByVoiceTakesAQuery(t *testing.T) {
	for _, path := range []string{"../../commands.id.toml", "../../commands.en.toml"} {
		set, err := intent.SetFromFile(path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		slotted := false
		for _, phrase := range set.PhrasesFor("search_music") {
			if strings.Contains(phrase, "{query}") {
				slotted = true
			}
		}
		if !slotted {
			t.Errorf("%s: no search_music phrase takes a {query}", path)
		}
	}
}
