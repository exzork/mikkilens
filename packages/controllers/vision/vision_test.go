package vision_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/exzork/mikkilens/packages/controllers/llm"
	"github.com/exzork/mikkilens/packages/controllers/vision"
	"github.com/exzork/mikkilens/packages/core/config"
	"github.com/exzork/mikkilens/packages/core/i18n"
)

// No network and no API key: the provider is a local stub, so what is actually
// tested is that the provider stays swappable, that answers are requested in
// her language, and that failures come back as something worth speaking aloud.

// stubProvider stands in for any OpenAI-compatible endpoint. It records the
// request it was sent and answers with whatever the test asked for.
type stubProvider struct {
	server   *httptest.Server
	received map[string]any
	answer   string
	status   int
}

func newStub(t *testing.T, answer string) *stubProvider {
	t.Helper()
	stub := &stubProvider{answer: answer, status: http.StatusOK}
	stub.server = httptest.NewServer(http.HandlerFunc(
		func(writer http.ResponseWriter, request *http.Request) {
			body, _ := io.ReadAll(request.Body)
			_ = json.Unmarshal(body, &stub.received)

			writer.Header().Set("Content-Type", "application/json")
			if stub.status != http.StatusOK {
				writer.WriteHeader(stub.status)
				_ = json.NewEncoder(writer).Encode(map[string]any{
					"error": map[string]any{"message": stub.answer},
				})
				return
			}
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"choices": []any{
					map[string]any{"message": map[string]any{"content": stub.answer}},
				},
			})
		}))
	t.Cleanup(stub.server.Close)
	return stub
}

func (s *stubProvider) messages(t *testing.T) []any {
	t.Helper()
	raw, ok := s.received["messages"].([]any)
	if !ok {
		t.Fatalf("the provider was not sent any messages: %v", s.received)
	}
	return raw
}

func configured(baseURL string, adjust ...func(*config.Config)) config.Config {
	settings := config.Default()
	settings.Model.Base = baseURL
	settings.Model.Model = "test-model"
	for _, change := range adjust {
		change(&settings)
	}
	return settings
}

func indonesian() *i18n.Locale { return i18n.Load("id") }

// -- provider independence ----------------------------------------------------

func TestTheEndpointComesEntirelyFromConfiguration(t *testing.T) {
	settings := configured("http://localhost:11434/v1", func(c *config.Config) {
		c.Model.Model = "llava"
	})
	endpoint := vision.New(settings, indonesian()).Endpoint()
	if endpoint.BaseURL != "http://localhost:11434/v1" || endpoint.Model != "llava" {
		t.Errorf("endpoint = %+v", endpoint)
	}
}

// One endpoint means exactly that: the screen and the chat summary go to the
// same place, and there is no second one to fall out of step with.
func TestTextAndImagesGoToTheSameEndpoint(t *testing.T) {
	settings := configured("https://example.test/v1")
	text := llm.New(settings, indonesian()).Endpoint()
	image := vision.New(settings, indonesian()).Endpoint()

	if text != image {
		t.Errorf("text endpoint %+v differs from the image one %+v", text, image)
	}
	if text.BaseURL != "https://example.test/v1" {
		t.Errorf("base = %q", text.BaseURL)
	}
}

func TestAnUnconfiguredEndpointIsRefused(t *testing.T) {
	controller := llm.New(config.Default(), indonesian())
	_, err := controller.Complete(context.Background(), nil, llm.Endpoint{}, 10)
	if err == nil {
		t.Error("an unconfigured endpoint must be refused")
	}
}

// TestALocalServerWithoutAKeyStillWorks covers Ollama and LM Studio, which
// accept any key or none at all.
func TestALocalServerWithoutAKeyStillWorks(t *testing.T) {
	stub := newStub(t, "OK")
	settings := configured(stub.server.URL)
	settings.Model.APIKeyEnv = "" // no key anywhere

	answer, err := vision.New(settings, indonesian()).
		DescribeImage(context.Background(), "hello", []byte("fake-jpeg"))
	if err != nil {
		t.Fatalf("DescribeImage: %v", err)
	}
	if answer != "OK" {
		t.Errorf("answer = %q", answer)
	}
}

// -- language -----------------------------------------------------------------

func TestAnswersAreRequestedInIndonesian(t *testing.T) {
	instruction := llm.New(configured("https://x/v1"), indonesian()).LanguageInstruction()
	if !strings.Contains(instruction, "Bahasa Indonesia") {
		t.Errorf("instruction = %q", instruction)
	}
}

func TestAnswersAreRequestedInEnglishForTheEnglishLocale(t *testing.T) {
	instruction := llm.New(configured("https://x/v1"), i18n.Load("en")).LanguageInstruction()
	if !strings.Contains(instruction, "English") {
		t.Errorf("instruction = %q", instruction)
	}
}

func TestTheInstructionForbidsMarkupThatReadsBadlyAloud(t *testing.T) {
	instruction := strings.ToLower(
		llm.New(configured("https://x/v1"), indonesian()).LanguageInstruction())
	if !strings.Contains(instruction, "markdown") || !strings.Contains(instruction, "emoji") {
		t.Errorf("instruction = %q", instruction)
	}
}

func TestTheVisionPromptCarriesTheLanguageAndTheImage(t *testing.T) {
	stub := newStub(t, "Ada kotak dialog error di tengah layar.")
	answer, err := vision.New(configured(stub.server.URL), indonesian()).
		DescribeImage(context.Background(), "apakah ada error", []byte("fake-jpeg-bytes"))
	if err != nil {
		t.Fatalf("DescribeImage: %v", err)
	}
	if !strings.Contains(answer, "error") {
		t.Errorf("answer = %q", answer)
	}

	messages := stub.messages(t)
	system, _ := messages[0].(map[string]any)["content"].(string)
	if !strings.Contains(system, "Bahasa Indonesia") {
		t.Errorf("the system prompt did not ask for Indonesian: %q", system)
	}

	parts, ok := messages[1].(map[string]any)["content"].([]any)
	if !ok || len(parts) != 2 {
		t.Fatalf("the user message is not a two-part list: %v", messages[1])
	}
	if text := parts[0].(map[string]any)["text"]; text != "apakah ada error" {
		t.Errorf("the question did not reach the model: %v", text)
	}
	url := parts[1].(map[string]any)["image_url"].(map[string]any)["url"].(string)
	if !strings.HasPrefix(url, "data:image/jpeg;base64,") {
		t.Errorf("the image was not attached as a data URL: %q", url[:min(40, len(url))])
	}
}

// -- capture ------------------------------------------------------------------

func TestCaptureDownscalesToTheConfiguredEdge(t *testing.T) {
	settings := configured("https://x/v1", func(c *config.Config) { c.Vision.MaxEdge = 640 })
	data, err := vision.New(settings, indonesian()).Capture()
	if err != nil {
		t.Skipf("no screen to capture in this environment: %v", err)
	}
	decoded, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("the capture is not a readable image: %v", err)
	}
	if format != "jpeg" {
		t.Errorf("format = %q, want jpeg", format)
	}
	bounds := decoded.Bounds()
	if bounds.Dx() > 640 || bounds.Dy() > 640 {
		t.Errorf("capture is %dx%d, want no edge above 640", bounds.Dx(), bounds.Dy())
	}
}

func TestCaptureReportsTheMonitorsItCanSee(t *testing.T) {
	info := vision.New(configured("https://x/v1"), indonesian()).CaptureInfo()
	if info.Monitors < 1 {
		t.Skip("no screen in this environment")
	}
	if info.Width <= 0 || info.Height <= 0 {
		t.Errorf("info = %+v", info)
	}
}

// -- answers ------------------------------------------------------------------

func TestALongAnswerIsShortenedForSpeaking(t *testing.T) {
	stub := newStub(t, strings.Repeat("x", 5000))
	settings := configured(stub.server.URL, func(c *config.Config) { c.Vision.MaxAnswerChars = 100 })

	answer, err := vision.New(settings, indonesian()).
		DescribeImage(context.Background(), "", []byte("x"))
	if err != nil {
		t.Fatalf("DescribeImage: %v", err)
	}
	if length := len([]rune(answer)); length > 101 {
		t.Errorf("answer is %d runes, want at most 101", length)
	}
	if !strings.HasSuffix(answer, "…") {
		t.Error("a shortened answer should end with an ellipsis")
	}
}

func TestAnEmptyAnswerIsAnErrorNotSilence(t *testing.T) {
	stub := newStub(t, "   ")
	_, err := vision.New(configured(stub.server.URL), indonesian()).
		DescribeImage(context.Background(), "", []byte("x"))
	if err == nil {
		t.Error("an empty answer must be an error, not silence")
	}
}

func TestDescribingWithoutAProviderIsRefused(t *testing.T) {
	_, err := vision.New(config.Default(), indonesian()).
		DescribeImage(context.Background(), "", []byte("x"))
	if err == nil {
		t.Error("expected a refusal")
	}
}

func TestSelfTestReportsAMissingProviderRatherThanFailing(t *testing.T) {
	result := vision.New(config.Default(), indonesian()).SelfTest(context.Background())
	if result.OK || result.Error == "" {
		t.Errorf("result = %+v", result)
	}
}

func TestSelfTestSendsARealImage(t *testing.T) {
	stub := newStub(t, "A red, a green and a blue square.")
	result := vision.New(configured(stub.server.URL), indonesian()).SelfTest(context.Background())
	if !result.OK {
		t.Fatalf("result = %+v", result)
	}

	parts := stub.messages(t)[1].(map[string]any)["content"].([]any)
	url := parts[1].(map[string]any)["image_url"].(map[string]any)["url"].(string)
	encoded := strings.TrimPrefix(url, "data:image/jpeg;base64,")
	if len(encoded) < 100 {
		t.Fatalf("the test card is suspiciously small: %d characters", len(encoded))
	}
	// It must be a real JPEG, or the test would pass against a broken encoder.
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := jpeg.Decode(bytes.NewReader(raw)); err != nil {
		t.Errorf("the test card is not a valid JPEG: %v", err)
	}
}

// -- error messages -----------------------------------------------------------

func TestProviderErrorsBecomeSpeakableIndonesian(t *testing.T) {
	cases := map[string]string{
		"Error code: 401 - unauthorized":       "API key ditolak",
		"Incorrect API key provided":           "API key ditolak",
		"Connection error: getaddrinfo failed": "tidak bisa terhubung ke server",
		"Request timed out":                    "server tidak menjawab",
	}
	for raw, want := range cases {
		if got := llm.Readable(raw); got != want {
			t.Errorf("Readable(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestAnUnrecognisedErrorIsPassedThroughButTrimmed(t *testing.T) {
	message := llm.Readable(strings.Repeat("y", 500))
	if length := len([]rune(message)); length == 0 || length > 160 {
		t.Errorf("message is %d runes", length)
	}
}

func TestARefusedKeyIsReportedInHerLanguage(t *testing.T) {
	stub := newStub(t, "Incorrect API key provided")
	stub.status = http.StatusUnauthorized

	_, err := vision.New(configured(stub.server.URL), indonesian()).
		DescribeImage(context.Background(), "", []byte("x"))
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "API key ditolak") {
		t.Errorf("error = %q", err)
	}
}
