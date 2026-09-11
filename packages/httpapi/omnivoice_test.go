package httpapi_test

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// The cloning endpoints, which are what the settings page calls so that adding
// a voice does not mean opening a file manager.
//
// Recording is not tested here and cannot be: it needs a microphone, and the
// stub engine deliberately has none. What is tested is everything either side
// of that -- the listing, the file path, the removal and, most of all, what
// happens to a name that was chosen to escape the folder it belongs in.

func TestOmniVoicesStartsEmptyRatherThanNull(t *testing.T) {
	server, _, _ := client(t)

	voices := getList(t, server, "/api/omnivoice/voices")
	if voices == nil {
		t.Fatal("the voice list came back as null; a page counting it would break")
	}
	if len(voices) != 0 {
		t.Errorf("a fresh machine has %d voices, want none", len(voices))
	}
}

// A file that is not a wav has to be refused with a reason, not saved and then
// discovered to be unreadable the first time she tries to speak.
func TestOmniUploadRefusesWhatIsNotAudio(t *testing.T) {
	server, _, _ := client(t)

	for _, test := range []struct {
		name  string
		body  map[string]any
		about string
	}{
		{"no name", map[string]any{
			"name": "", "audio": base64.StdEncoding.EncodeToString(testWAV(24000)),
		}, "a voice with no name"},
		{"no audio", map[string]any{"name": "mikki", "audio": ""}, "an empty file"},
		{"not base64", map[string]any{"name": "mikki", "audio": "!!!not base64!!!"}, "damaged bytes"},
		{"not a wav", map[string]any{
			"name": "mikki", "audio": base64.StdEncoding.EncodeToString([]byte("hello")),
		}, "a file that is not a wav"},
	} {
		t.Run(test.name, func(t *testing.T) {
			status, body := send(t, server, http.MethodPost, "/api/omnivoice/upload", test.body)
			if status == http.StatusOK {
				t.Fatalf("%s was accepted", test.about)
			}
			if detail, _ := body["detail"].(string); detail == "" {
				t.Errorf("%s was refused with no reason given", test.about)
			}
		})
	}
}

// The one that matters. A voice name becomes a filename, and it arrives over
// an HTTP API that anything on the machine can reach.
func TestOmniUploadCannotWriteOutsideItsFolder(t *testing.T) {
	server, _, directory := client(t)

	canary := filepath.Join(directory, "data", "config.toml")
	if err := os.MkdirAll(filepath.Dir(canary), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(canary, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, hostile := range []string{
		"../../config",
		"..\\..\\config",
		"../config",
		"/config",
		"models/../../config",
	} {
		// The upload will fail anyway on this machine -- there is no encoder
		// to prepare the voice with -- so the assertion is not about the
		// status. It is that nothing outside the voices folder was touched.
		send(t, server, http.MethodPost, "/api/omnivoice/upload", map[string]any{
			"name":  hostile,
			"audio": base64.StdEncoding.EncodeToString(testWAV(24000)),
		})

		contents, err := os.ReadFile(canary)
		if err != nil {
			t.Fatalf("the name %q removed a file outside the voices folder", hostile)
		}
		if string(contents) != "original" {
			t.Fatalf("the name %q overwrote a file outside the voices folder", hostile)
		}
	}

	// Nor may anything have been created above the voices folder.
	strays, _ := filepath.Glob(filepath.Join(directory, "data", "*.wav"))
	if len(strays) > 0 {
		t.Errorf("files were written outside the voices folder: %v", strays)
	}
}

func TestOmniRemoveNeedsAName(t *testing.T) {
	server, _, _ := client(t)

	status, _ := send(t, server, http.MethodDelete, "/api/omnivoice/voices", nil)
	if status != http.StatusBadRequest {
		t.Errorf("removing nothing in particular returned %d, want 400", status)
	}

	status, body := send(t, server,
		http.MethodDelete, "/api/omnivoice/voices?name=never-existed", nil)
	if status == http.StatusOK {
		t.Error("removing a voice that does not exist reported success")
	}
	if detail, _ := body["detail"].(string); detail == "" {
		t.Error("removing a voice that does not exist gave no reason")
	}
}

// Recording needs the microphone the engine owns, and the stub has none. The
// point of the test is that this is a clear refusal rather than a panic or a
// hang -- somebody with no input device selected will hit exactly this.
func TestOmniRecordSaysWhenThereIsNoMicrophone(t *testing.T) {
	server, _, _ := client(t)

	status, body := send(t, server, http.MethodPost, "/api/omnivoice/record",
		map[string]any{"name": "mikki", "text": "halo"})
	if status == http.StatusOK {
		t.Fatal("recording succeeded with no microphone")
	}
	if detail, _ := body["detail"].(string); detail == "" {
		t.Error("recording without a microphone gave no reason")
	}
}

func TestOmniVoicesRefusesTheWrongMethod(t *testing.T) {
	server, _, _ := client(t)

	status, _ := send(t, server, http.MethodPut, "/api/omnivoice/voices", map[string]any{})
	if status != http.StatusMethodNotAllowed {
		t.Errorf("PUT returned %d, want 405", status)
	}
}

// -- helpers -------------------------------------------------------------------

// getList is get() for the endpoints that answer with an array rather than an
// object, which the shared helper cannot decode.
func getList(t *testing.T, server *httptest.Server, path string) []map[string]any {
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
		t.Fatalf("decoding %s: %v", path, err)
	}
	return payload
}

// testWAV is a second of a tone, as a real 24 kHz mono wav.
func testWAV(samples int) []byte {
	body := make([]byte, 0, samples*2)
	for index := 0; index < samples; index++ {
		value := math.Sin(float64(index) * 0.05)
		body = binary.LittleEndian.AppendUint16(body, uint16(int16(value*20000)))
	}

	out := make([]byte, 0, len(body)+44)
	out = append(out, "RIFF"...)
	out = binary.LittleEndian.AppendUint32(out, uint32(36+len(body)))
	out = append(out, "WAVEfmt "...)
	out = binary.LittleEndian.AppendUint32(out, 16)
	out = binary.LittleEndian.AppendUint16(out, 1)
	out = binary.LittleEndian.AppendUint16(out, 1)
	out = binary.LittleEndian.AppendUint32(out, 24000)
	out = binary.LittleEndian.AppendUint32(out, 48000)
	out = binary.LittleEndian.AppendUint16(out, 2)
	out = binary.LittleEndian.AppendUint16(out, 16)
	out = append(out, "data"...)
	out = binary.LittleEndian.AppendUint32(out, uint32(len(body)))
	return append(out, body...)
}
