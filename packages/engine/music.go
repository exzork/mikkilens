package engine

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/exzork/mikkilens/packages/audio/feedback"
	"github.com/exzork/mikkilens/packages/controllers/music"
	"github.com/exzork/mikkilens/packages/core/i18n"
	"github.com/exzork/mikkilens/packages/core/intent"
)

// Finding a song and playing it, without seeing either.
//
// This is the one command whose input is typed rather than spoken, and that is
// a decision about accuracy rather than a concession. Recognition is tuned for
// short sentences in her language; song and artist names are neither. "Hindia
// Evaluasi" and "Sisitipsi Buih Jadi Permadani" go through a speech model as
// approximately anything, and the failure is not a helpful one -- she gets
// five wrong songs and no way to tell whether she was misheard or the search
// was.
//
// She types perfectly without looking, which makes the keyboard the more
// accurate instrument here and the microphone the compromise. So the desktop
// app opens a box on a key press, she types the name, and everything from
// there is spoken: the results one at a time, then a number to play one.
//
// Speaking it still works -- "cari lagu monokrom" searches for monokrom -- for
// the same reason the web search has a spoken path: whatever else is or is not
// running, the thing she said out loud must do something.
//
// Playing means opening it in YouTube Music in her browser. MikkiLens has no
// player of its own and should not grow one: she has an account, a history and
// a subscription there already, and a second player would be a second thing to
// control by voice for no gain.

// resultsKeptFor is how long a set of results stays pickable.
//
// Long enough to hear five of them, think, and answer; short enough that "play
// number two" an hour later does not start a song she was looking at before
// lunch. Past it the results are forgotten and she is told to search again,
// which is a better answer than a surprise.
const resultsKeptFor = 10 * time.Minute

// musicSearchTimeout bounds the lookup. The package has its own, shorter
// deadline; this is the backstop.
const musicSearchTimeout = 20 * time.Second

// typing is the request for the box she types a song name into.
//
// The engine cannot open a window -- it has none, and on a headless run there
// is nothing to open one in. So the desktop app waits here, and asking it to
// open is a matter of waking whoever is waiting.
//
// The count is what makes the wait honest across a reconnect: a waiter says
// which one it last saw, and a request that arrived while it was reconnecting
// is answered immediately rather than lost. And when nobody is waiting at all,
// that is knowable rather than guessed at, which is the difference between
// telling her to press the key and telling her to say the name instead.
type typing struct {
	mu      sync.Mutex
	cond    *sync.Cond
	count   int
	waiting int
	seen    time.Time
}

func (t *typing) init() {
	if t.cond == nil {
		t.cond = sync.NewCond(&t.mu)
	}
}

// windowGrace is how long a window counts as still being there after its last
// wait ended.
//
// A long poll comes back every half a minute or so and is immediately asked
// again, and in the gap between the two nothing is waiting. Without this, a
// request landing in that gap would be told there is no window -- and then open
// one anyway a moment later, which is both answers at once. Comfortably longer
// than the poll, and short enough that a window closed a minute ago is
// correctly reported as gone.
const windowGrace = time.Minute

// request asks for the box, and reports whether anything is there to open it.
func (t *typing) request() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.init()

	t.count++
	t.cond.Broadcast()
	return t.waiting > 0 || time.Since(t.seen) < windowGrace
}

// wait blocks until the count moves past since, or until ctx is done. It
// returns the count either way, so a caller that timed out comes back asking
// about the right one.
func (t *typing) wait(ctx context.Context, since int) int {
	t.mu.Lock()
	t.init()
	t.waiting++
	t.seen = time.Now()
	t.mu.Unlock()

	// Broadcast on the caller's behalf when its context ends, so a window that
	// closed does not leave a goroutine parked on the condition for ever.
	stop := context.AfterFunc(ctx, func() {
		t.mu.Lock()
		defer t.mu.Unlock()
		t.init()
		t.cond.Broadcast()
	})
	defer stop()

	t.mu.Lock()
	defer t.mu.Unlock()
	for t.count <= since && ctx.Err() == nil {
		t.cond.Wait()
	}
	t.waiting--
	t.seen = time.Now()
	return t.count
}

// results is the last search, waiting to be picked from.
type results struct {
	mu    sync.Mutex
	songs []music.Song
	found time.Time
}

// remember replaces what is pickable.
func (r *results) remember(songs []music.Song) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.songs, r.found = songs, time.Now()
}

// current is what can still be picked, and whether anything can.
func (r *results) current() ([]music.Song, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if len(r.songs) == 0 || time.Since(r.found) > resultsKeptFor {
		return nil, false
	}
	return append([]music.Song(nil), r.songs...), true
}

func musicHandlers(e *Engine) map[string]intent.Handler {
	return map[string]intent.Handler{
		"search_music": e.searchMusicCommand,
		"play_song":    e.playSongCommand,
		"list_songs":   e.listSongsCommand,
	}
}

// Songs is what the last search found, for the typing window to show. Empty
// when there is nothing pickable, which is the same thing the voice path says
// out loud.
func (e *Engine) Songs() []music.Song {
	songs, _ := e.songs.current()
	return songs
}

// -- searching ----------------------------------------------------------------

// searchMusicCommand is the spoken path: she said the song name outright.
func (e *Engine) searchMusicCommand(slots map[string]string) error {
	query := trimmed(slots["query"])
	if query == "" {
		query = trimmed(slots["text"])
	}
	if query == "" {
		// Nothing after the command, so this is the hands-free half of it: ask
		// for the box and let her type the name, which is the accurate way in
		// anyway. What she is told depends on whether there is a window to open
		// it in -- being told to press a key that does nothing is worse than
		// being told to say the name instead.
		if e.RequestTyping() {
			e.bus.SayKey("music.type_it", feedback.Result)
		} else {
			e.bus.SayKey("music.nothing_asked", feedback.Result,
				i18n.Args{"key": spokenCombination(e.Config().Music.Combination)})
		}
		return nil
	}
	e.SearchMusic(query)
	return nil
}

// RequestTyping asks the desktop app to open the box she types into, and
// reports whether there was one listening.
func (e *Engine) RequestTyping() bool { return e.typing.request() }

// WaitForTyping blocks until the box is asked for, and answers with the count
// to ask about next time. The desktop app sits in this; nothing else should.
func (e *Engine) WaitForTyping(ctx context.Context, since int) int {
	return e.typing.wait(ctx, since)
}

// SearchMusic looks a song up and reads back what it found, without waiting.
//
// The voice path uses this: the caller is a command handler, and holding one
// open across a network round trip holds the microphone with it. What she gets
// back is spoken, which is the whole answer.
func (e *Engine) SearchMusic(query string) {
	go func() {
		defer func() {
			if problem := recover(); problem != nil {
				slog.Error("the music search panicked", "query", query, "panic", problem)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), musicSearchTimeout)
		defer cancel()
		_, _ = e.FindSongs(ctx, query)
	}()
}

// FindSongs searches, remembers what came back, and reads it out.
//
// The typing window calls this and waits, because it has a list to show as
// well as one to be read; the spoken path calls it from a goroutine. Both get
// the same sentences said in the same order, because there is one wording per
// outcome and it does not fork depending on who asked.
//
// An error here has already been spoken by the time it is returned. It is
// returned as well so the window can stop looking like it is still searching.
func (e *Engine) FindSongs(ctx context.Context, query string) ([]music.Song, error) {
	query = trimmed(query)
	if query == "" {
		e.bus.SayKey("music.nothing_asked", feedback.Result,
			i18n.Args{"key": spokenCombination(e.Config().Music.Combination)})
		return nil, &music.Error{Reason: "there was nothing to search for"}
	}

	// Announced before the round trip, not after: a couple of seconds of
	// silence after she pressed Enter reads as having been ignored, and the
	// window she typed into has closed by then.
	e.bus.SayKey("music.searching", feedback.Result, i18n.Args{"query": query})

	language := e.Config().Language.Output
	songs, err := music.Search(ctx, query, language, regionFor(language))
	if err != nil {
		slog.Warn("the music search failed", "query", query, "error", err)
		e.bus.SayKey("music.failed", feedback.Error)
		return nil, err
	}
	if len(songs) == 0 {
		e.bus.SayKey("music.nothing_found", feedback.Result, i18n.Args{"query": query})
		return nil, nil
	}

	e.songs.remember(songs)
	e.readResults(songs)
	return songs, nil
}

// listSongsCommand reads the last results again.
//
// Worth its own command rather than making her search twice. Five songs is
// more than one sentence's worth of listening, and a superchat landing in the
// middle of them is enough to lose which one was number four.
func (e *Engine) listSongsCommand(map[string]string) error {
	songs, ok := e.songs.current()
	if !ok {
		e.bus.SayKey("music.nothing_to_play", feedback.Result)
		return nil
	}
	e.readResults(songs)
	return nil
}

// readResults says the results one at a time.
//
// One utterance per song rather than one long sentence, deliberately. They
// queue separately, so they are read with a breath between them and can be
// followed; an error or a confirmation preempts at the next gap rather than
// waiting out the whole list; and being cut off loses the rest of the list
// rather than the whole of it.
func (e *Engine) readResults(songs []music.Song) {
	e.bus.SayKey("music.found", feedback.Result, i18n.Args{"count": len(songs)})

	for index, song := range songs {
		e.bus.Say(e.describe(index+1, song), feedback.Result)
	}
	e.bus.SayKey("music.pick", feedback.Result, i18n.Args{"count": len(songs)})
}

// describe is one result as a sentence.
//
// The running time is said as minutes and seconds rather than relayed as
// YouTube Music writes it, because how it writes it depends on the language:
// "6:10" in English and "6.10" in Indonesian, and a voice handed the second
// one says "six point one zero".
func (e *Engine) describe(number int, song music.Song) string {
	locale := e.Locale()
	args := i18n.Args{
		"number": number,
		"title":  song.Title,
		"artist": song.Artist,
	}
	if !song.HasDuration() {
		return locale.T("music.result", args)
	}
	args["minutes"] = song.Minutes
	args["seconds"] = song.Seconds
	return locale.T("music.result_timed", args)
}

// -- playing ------------------------------------------------------------------

// playSongCommand is "play number two", spoken.
func (e *Engine) playSongCommand(slots map[string]string) error {
	spoken := trimmed(slots["number"])
	number, ok := e.spokenNumber(spoken)
	if !ok {
		e.bus.SayKey("music.not_a_number", feedback.Error, i18n.Args{"number": spoken})
		return nil
	}
	_, _ = e.PlaySong(number)
	return nil
}

// PlaySong opens the nth result, counting from one, and says which it was.
//
// Every ending speaks. A number with nothing behind it, a search that has been
// forgotten, a machine with no browser to open it in: each of those is a
// different thing to do next, and silence is indistinguishable from a song
// playing somewhere she cannot hear it.
func (e *Engine) PlaySong(number int) (music.Song, error) {
	songs, ok := e.songs.current()
	if !ok {
		e.bus.SayKey("music.nothing_to_play", feedback.Result)
		return music.Song{}, &music.Error{Reason: "there are no results to play"}
	}
	if number < 1 || number > len(songs) {
		e.bus.SayKey("music.no_such_result", feedback.Error,
			i18n.Args{"number": number, "count": len(songs)})
		return music.Song{}, &music.Error{Reason: "there is no result with that number"}
	}

	song := songs[number-1]
	if !e.openInBrowser(song.URL()) {
		e.bus.SayKey("music.no_browser", feedback.Error)
		return music.Song{}, &music.Error{Reason: "there is no browser to play it in"}
	}
	e.bus.SayKey("music.playing", feedback.Result,
		i18n.Args{"title": song.Title, "artist": song.Artist})
	return song, nil
}

// openInBrowser is openBrowser, with an answer about whether there was one.
//
// The sign-in flow can afford to warn into the log and carry on, because she
// is looking at the settings page when it runs. This cannot: a played song is
// meant to start playing, and nothing else on the machine will say that it did
// not.
func (e *Engine) openInBrowser(url string) bool {
	e.mu.RLock()
	open := e.OpenBrowser
	e.mu.RUnlock()

	if open == nil {
		slog.Error("no way to open a browser; the song cannot be played", "url", url)
		return false
	}
	open(url)
	return true
}

// spokenNumber reads a position out of what she said.
//
// Recognition gives back "2" or "dua" or "two" depending on the model, the
// language and the day, and a command that only accepts one of those is a
// command that works some of the time. So digits and the language's own number
// words both count, and the words are looked for inside the phrase rather than
// matched against the whole of it -- "nomor dua" and "yang kedua" both arrive
// here when a phrase matched loosely.
func (e *Engine) spokenNumber(spoken string) (int, bool) {
	spoken = strings.ToLower(trimmed(spoken))
	if spoken == "" {
		return 0, false
	}

	if number, ok := digits(spoken); ok {
		return number, true
	}
	for index, word := range e.Locale().NumberWords() {
		if word != "" && containsWord(spoken, word) {
			return index + 1, true
		}
	}
	return 0, false
}

// digits reads a bare number, and only a bare one: a phrase with a digit
// buried in it is more likely a title than a position.
func digits(spoken string) (int, bool) {
	number := 0
	for _, character := range spoken {
		if character < '0' || character > '9' {
			return 0, false
		}
		number = number*10 + int(character-'0')
		if number > music.Limit {
			return 0, false
		}
	}
	return number, number > 0
}

// containsWord reports whether a word appears in a phrase on its own, so
// "dua" does not match inside "duaratus".
func containsWord(phrase, word string) bool {
	for _, field := range strings.Fields(phrase) {
		if strings.Trim(field, ".,!?") == word {
			return true
		}
	}
	return false
}

// spokenCombination turns a key as config.toml writes it into something worth
// hearing.
//
// "<ctrl>+<alt>+<f>" read out is "less than c t r l greater than plus less
// than a l t greater than" -- which is not a key she can find, and is the kind
// of sentence that makes an application sound broken. The brackets and the
// pluses come out, the abbreviations are spelled the way they are said, and
// what is left is three words.
func spokenCombination(combination string) string {
	spoken := map[string]string{
		"ctrl": "Control", "control": "Control",
		"alt": "Alt", "alt_gr": "Alt Gr",
		"shift": "Shift",
		"cmd":   "Windows", "win": "Windows", "super": "Windows",
		"space": "Space", "enter": "Enter", "return": "Enter",
		"esc": "Escape", "escape": "Escape", "tab": "Tab",
		"page_up": "Page Up", "page_down": "Page Down",
		"print_screen": "Print Screen",
	}

	parts := []string{}
	for _, raw := range strings.Split(combination, "+") {
		name := strings.ToLower(strings.TrimSpace(raw))
		name = strings.TrimSuffix(strings.TrimPrefix(name, "<"), ">")
		if name == "" {
			continue
		}
		if word, known := spoken[name]; known {
			parts = append(parts, word)
			continue
		}
		// A letter, a digit, or a name nobody thought to translate. Underscores
		// become spaces so "num_5" is two words rather than one unsayable one,
		// and the first letter is capitalised so a voice treats it as a name.
		name = strings.ReplaceAll(name, "_", " ")
		parts = append(parts, strings.ToUpper(name[:1])+name[1:])
	}
	return strings.Join(parts, " ")
}

// regionFor is which country's YouTube Music to ask.
//
// It changes the answer, and not by a little: the same query ranks differently
// in Indonesia than in the United States, and hers is the one that should
// decide. Taken from the language she has chosen rather than asked for
// separately, because it is one more field to read out for a setting that has
// one sensible value.
func regionFor(language string) string {
	switch strings.ToLower(strings.TrimSpace(language)) {
	case "id":
		return "ID"
	default:
		// English is not a country, and neither is any language code added
		// later. The United States is where YouTube Music's default catalogue
		// lives, so an unmapped language gets the answer everyone gets rather
		// than a region code that is not one.
		return "US"
	}
}
