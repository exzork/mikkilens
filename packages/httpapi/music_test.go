package httpapi_test

import (
	"net/http"
	"testing"
)

// The routes behind the typing window, and the one behind the mute key.
//
// What the window needs from these is an answer it can render and stop
// spinning on. What she needs is that the same request also speaks, which the
// engine does; these check the wiring that carries the request to it.

func TestSearchingForMusicAnswersWithWhatWasFound(t *testing.T) {
	server, engine, _ := client(t)

	status, payload := send(t, server, http.MethodPost, "/api/music/search",
		map[string]any{"query": "tulus monokrom"})
	if status != http.StatusOK {
		t.Fatalf("searching returned %d: %v", status, payload)
	}

	if len(engine.searched) != 1 || engine.searched[0] != "tulus monokrom" {
		t.Fatalf("the engine was asked for %v", engine.searched)
	}

	songs, ok := payload["songs"].([]any)
	if !ok || len(songs) != 1 {
		t.Fatalf("songs = %v", payload["songs"])
	}
	first, _ := songs[0].(map[string]any)
	if first["title"] != "Monokrom" || first["artist"] != "Tulus" {
		t.Errorf("first result = %v", first)
	}
	// Minutes and seconds are carried apart from the written duration, because
	// the written one is "3.35" in Indonesian and a voice reads that as a
	// decimal.
	if first["minutes"] != float64(3) || first["seconds"] != float64(35) {
		t.Errorf("running time = %v and %v", first["minutes"], first["seconds"])
	}
}

// A search that finds nothing is not a failure. The window has to show an
// empty list rather than an error, and never a JSON null it has to defend
// against.
func TestASearchThatFindsNothingIsStillAnAnswer(t *testing.T) {
	server, _, _ := client(t)

	status, payload := send(t, server, http.MethodPost, "/api/music/search",
		map[string]any{"query": "nothing"})
	if status != http.StatusOK {
		t.Fatalf("searching returned %d: %v", status, payload)
	}
	songs, ok := payload["songs"].([]any)
	if !ok {
		t.Fatalf("songs = %v, want an empty list", payload["songs"])
	}
	if len(songs) != 0 {
		t.Errorf("got %d songs, want none", len(songs))
	}
}

func TestAnEmptyQueryIsRefused(t *testing.T) {
	server, _, _ := client(t)

	status, payload := send(t, server, http.MethodPost, "/api/music/search",
		map[string]any{"query": "   "})
	if status == http.StatusOK {
		t.Fatalf("an empty query was accepted: %v", payload)
	}
	if payload["detail"] == "" {
		t.Error("no reason was given")
	}
}

// A window opened again should offer what she can still pick from, rather than
// an empty box she has to search in twice.
func TestTheLastResultsCanBeReadBack(t *testing.T) {
	server, _, _ := client(t)

	if payload := get(t, server, "/api/music/songs"); len(payload["songs"].([]any)) != 0 {
		t.Fatalf("something was pickable before any search: %v", payload["songs"])
	}

	send(t, server, http.MethodPost, "/api/music/search", map[string]any{"query": "tulus"})

	payload := get(t, server, "/api/music/songs")
	if len(payload["songs"].([]any)) != 1 {
		t.Errorf("songs = %v", payload["songs"])
	}
}

func TestPlayingAResultAnswersWithWhatItPlayed(t *testing.T) {
	server, engine, _ := client(t)
	send(t, server, http.MethodPost, "/api/music/search", map[string]any{"query": "tulus"})

	status, payload := send(t, server, http.MethodPost, "/api/music/play",
		map[string]any{"number": 1})
	if status != http.StatusOK {
		t.Fatalf("playing returned %d: %v", status, payload)
	}
	if len(engine.played) != 1 || engine.played[0] != 1 {
		t.Fatalf("the engine was asked to play %v", engine.played)
	}
	song, _ := payload["song"].(map[string]any)
	if song["title"] != "Monokrom" {
		t.Errorf("played %v", song)
	}
}

// The numbers are the ones she heard read out, so number four out of one
// result has to be refused rather than clamped to something that plays.
func TestPlayingANumberThatIsNotThereIsRefused(t *testing.T) {
	server, _, _ := client(t)
	send(t, server, http.MethodPost, "/api/music/search", map[string]any{"query": "tulus"})

	status, payload := send(t, server, http.MethodPost, "/api/music/play",
		map[string]any{"number": 4})
	if status == http.StatusOK {
		t.Fatalf("number four out of one result was accepted: %v", payload)
	}
}

// -- the mute -----------------------------------------------------------------

func TestMuteCanBeReadAndSet(t *testing.T) {
	server, engine, _ := client(t)

	if payload := get(t, server, "/api/mute"); payload["muted"] != false {
		t.Fatalf("chat started out muted: %v", payload)
	}

	status, payload := send(t, server, http.MethodPost, "/api/mute", map[string]any{"muted": true})
	if status != http.StatusOK || payload["muted"] != true {
		t.Fatalf("muting returned %d: %v", status, payload)
	}
	if !engine.muted {
		t.Error("the engine was not told to mute")
	}

	if payload := get(t, server, "/api/mute"); payload["muted"] != true {
		t.Errorf("the mute did not stick: %v", payload)
	}
}

// A body with no "muted" in it toggles. The key uses that: a key that had to
// read the state first is a key that can be wrong about it.
func TestMuteWithoutAValueToggles(t *testing.T) {
	server, engine, _ := client(t)

	if _, payload := send(t, server, http.MethodPost, "/api/mute", map[string]any{}); payload["muted"] != true {
		t.Fatalf("the first toggle gave %v", payload)
	}
	if _, payload := send(t, server, http.MethodPost, "/api/mute", map[string]any{}); payload["muted"] != false {
		t.Fatalf("the second toggle gave %v", payload)
	}
	if engine.mutes != 2 {
		t.Errorf("the engine was asked %d times, want 2", engine.mutes)
	}
}
