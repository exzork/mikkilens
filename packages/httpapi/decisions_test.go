package httpapi_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
)

// The settings page can only show what the config API hands it, and can only
// save what the API takes back. Both directions are easy to break from the Go
// side without anything failing to compile, and the symptom is a section of
// the page that quietly does nothing -- so both are checked here.

// putJSON sends a partial config the way the page does.
func putJSON(t *testing.T, url string, body any) *http.Response {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { response.Body.Close() })
	return response
}

// Everything the page draws a box for has to arrive, including the name of the
// variable the key is kept under -- without which Save has nowhere to put it.
func TestTheConfigAPIOffersEveryDecisionSetting(t *testing.T) {
	server, _, _ := client(t)

	decisions, ok := get(t, server, "/api/config")["decisions"].(map[string]any)
	if !ok {
		t.Fatal("GET /api/config has no decisions section for the page to show")
	}

	for _, field := range []string{
		"enabled", "base_url", "model", "api_key_env", "min_confidence", "timeout_s",
	} {
		if _, present := decisions[field]; !present {
			t.Errorf("%q is missing, so the page cannot show it", field)
		}
	}
}

// Off, and pointed at Jev. The address and the model are not hers to look up,
// so they ship filled in and switching it on is the only decision left.
func TestDecisionsArriveOffButFilledIn(t *testing.T) {
	server, _, _ := client(t)

	decisions, _ := get(t, server, "/api/config")["decisions"].(map[string]any)

	if decisions["enabled"] != false {
		t.Errorf("enabled is %v, want false: this must be opt-in", decisions["enabled"])
	}
	if decisions["model"] != "~typesafe/jev-latest" {
		t.Errorf("model is %v", decisions["model"])
	}
	if decisions["api_key_env"] == "" {
		t.Error("no api_key_env, so the page has nowhere to save the key")
	}
}

// Saving from the page has to reach the engine, or the settings appear to save
// and change nothing until the next restart.
func TestSavingDecisionsFromThePageReachesTheEngine(t *testing.T) {
	server, engine, _ := client(t)

	response := putJSON(t, server.URL+"/api/config", map[string]any{
		"decisions": map[string]any{
			"enabled":        true,
			"base_url":       "https://openrouter.ai/api/alpha",
			"model":          "typesafe/jev-1.13",
			"min_confidence": 0.85,
			"timeout_s":      4.0,
		},
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("PUT /api/config returned %s", response.Status)
	}

	saved := engine.Config().Decisions
	if !saved.Enabled {
		t.Error("enabled did not reach the engine")
	}
	if saved.Model != "typesafe/jev-1.13" {
		t.Errorf("model is %q", saved.Model)
	}
	if saved.MinConfidence != 0.85 {
		t.Errorf("min_confidence is %v, want 0.85", saved.MinConfidence)
	}
	if saved.TimeoutS != 4.0 {
		t.Errorf("timeout_s is %v, want 4", saved.TimeoutS)
	}
}

// Saving one section must not disturb another. The page saves [decisions] on
// its own button, and the model endpoint has to survive it untouched.
func TestSavingDecisionsLeavesTheModelEndpointAlone(t *testing.T) {
	server, engine, _ := client(t)

	putJSON(t, server.URL+"/api/config", map[string]any{
		"model": map[string]any{"base_url": "http://localhost:11434/v1", "model": "gemma3n:e2b"},
	})
	putJSON(t, server.URL+"/api/config", map[string]any{
		"decisions": map[string]any{"enabled": true},
	})

	settings := engine.Config()
	if settings.Model.Base != "http://localhost:11434/v1" || settings.Model.Model != "gemma3n:e2b" {
		t.Errorf("the model endpoint was disturbed: %+v", settings.Model)
	}
	if !settings.Decisions.Enabled {
		t.Error("the decision section did not save")
	}
}

// Pressing Test with nothing configured has to answer, and say why, rather
// than hanging or returning an error status the page shows as a stack trace.
func TestTestingAnUnconfiguredDecisionModelExplainsItself(t *testing.T) {
	server, _, _ := client(t)

	response, err := http.Post(server.URL+"/api/test/decisions", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("returned %s; the page expects an answer it can read", response.Status)
	}

	var answer struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.NewDecoder(response.Body).Decode(&answer); err != nil {
		t.Fatal(err)
	}
	if answer.OK {
		t.Error("reported success with nothing configured")
	}
	if answer.Error == "" {
		t.Error("failed without saying why")
	}
}
