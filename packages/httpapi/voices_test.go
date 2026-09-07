package httpapi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// The voice dropdown is filled from this endpoint, and the two engines do not
// share a naming scheme -- so asking for one engine and being given the other
// one's names is a dropdown full of voices that cannot be used.

func voices(t *testing.T, server *httptest.Server, path string) []map[string]any {
	t.Helper()
	response, err := http.Get(server.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET %s returned %s", path, response.Status)
	}

	var payload []map[string]any
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return payload
}

// A voice is a file. Dropping one in is all it takes to have it offered, which
// is what makes a voice built elsewhere work with no code change.
func TestTheLocalVoicesAreWhicheverFilesAreThere(t *testing.T) {
	server, _, directory := client(t)

	styles := filepath.Join(directory, "data", "models", "supertonic", "voice_styles")
	if err := os.MkdirAll(styles, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"F1", "M2", "mikki-custom"} {
		if err := os.WriteFile(filepath.Join(styles, name+".json"), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	found := voices(t, server, "/api/voices?engine=local")
	if len(found) != 3 {
		t.Fatalf("got %d voices, want 3: %v", len(found), found)
	}

	byName := map[string]string{}
	for _, voice := range found {
		name, _ := voice["name"].(string)
		gender, _ := voice["gender"].(string)
		byName[name] = gender
	}
	if byName["F1"] != "Female" {
		t.Errorf("F1 is %q, want Female", byName["F1"])
	}
	if byName["M2"] != "Male" {
		t.Errorf("M2 is %q, want Male", byName["M2"])
	}
	if _, ok := byName["mikki-custom"]; !ok {
		t.Error("a voice built elsewhere was not offered")
	}
	// And nothing is claimed about a voice somebody named themselves.
	if byName["mikki-custom"] != "" {
		t.Errorf("mikki-custom is %q, want no claim about it", byName["mikki-custom"])
	}
}

// With nothing installed the list is empty rather than an error, so the page
// shows a dropdown it can explain instead of failing to load.
func TestNoLocalVoicesIsAnEmptyListRatherThanAFailure(t *testing.T) {
	server, _, _ := client(t)

	if found := voices(t, server, "/api/voices?engine=local"); len(found) != 0 {
		t.Errorf("got %v, want nothing", found)
	}
}

// The Windows synthesizer reads in whatever voice Windows is set to. There is
// nothing to choose, and saying so honestly beats a list it would ignore.
func TestTheWindowsEngineOffersNothingToChoose(t *testing.T) {
	server, _, _ := client(t)

	if found := voices(t, server, "/api/voices?engine=windows"); len(found) != 0 {
		t.Errorf("got %v, want nothing", found)
	}
}

// No engine in the query means the configured one, which by default is local.
func TestNoEngineInTheQueryMeansTheConfiguredOne(t *testing.T) {
	server, _, directory := client(t)

	styles := filepath.Join(directory, "data", "models", "supertonic", "voice_styles")
	if err := os.MkdirAll(styles, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(styles, "F3.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	found := voices(t, server, "/api/voices")
	if len(found) != 1 || found[0]["name"] != "F3" {
		t.Errorf("got %v, want the local voice list", found)
	}
}
