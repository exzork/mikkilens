package stt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"github.com/exzork/mikkilens/packages/core/config"
)

// remote sends audio to any endpoint that speaks the OpenAI transcription API.
//
// The point is the same as everywhere else in MikkiLens: the provider is
// configuration, not code. A local faster-whisper-server, a self-hosted
// whisper.cpp server, OpenAI, or Groq are all a base_url change. Nothing else
// in the app knows the difference.
type remote struct {
	baseURL string
	model   string
	apiKey  string
	client  *http.Client
}

func newRemote(settings config.STT) (Backend, error) {
	if settings.BaseURL == "" {
		return nil, &Error{Reason: "no transcription endpoint is configured"}
	}
	model := settings.Model
	if model == "" {
		model = "whisper-1"
	}
	return &remote{
		baseURL: strings.TrimRight(settings.BaseURL, "/"),
		model:   model,
		apiKey:  config.ResolveSecret(settings.APIKeyEnv),
		client:  &http.Client{Timeout: 60 * time.Second},
	}, nil
}

func (r *remote) Name() string { return fmt.Sprintf("%s at %s", r.model, r.baseURL) }

// Load has nothing to prepare: the first request is the test.
func (r *remote) Load(context.Context) error { return nil }

func (r *remote) Close() error { return nil }

func (r *remote) Transcribe(ctx context.Context, audio []float32, language string) (Transcript, error) {
	body := &bytes.Buffer{}
	form := multipart.NewWriter(body)

	part, err := form.CreateFormFile("file", "utterance.wav")
	if err != nil {
		return Transcript{}, &Error{Reason: err.Error()}
	}
	if _, err := part.Write(encodeWAV(audio, SampleRate)); err != nil {
		return Transcript{}, &Error{Reason: err.Error()}
	}
	_ = form.WriteField("model", r.model)
	if language != "" && language != "auto" {
		_ = form.WriteField("language", language)
	}
	_ = form.WriteField("response_format", "json")
	if err := form.Close(); err != nil {
		return Transcript{}, &Error{Reason: err.Error()}
	}

	request, err := http.NewRequestWithContext(
		ctx, http.MethodPost, r.baseURL+"/audio/transcriptions", body)
	if err != nil {
		return Transcript{}, &Error{Reason: err.Error()}
	}
	request.Header.Set("Content-Type", form.FormDataContentType())
	if r.apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+r.apiKey)
	}

	response, err := r.client.Do(request)
	if err != nil {
		return Transcript{}, &Error{Reason: "could not reach the transcription server"}
	}
	defer response.Body.Close()

	payload, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return Transcript{}, &Error{Reason: err.Error()}
	}
	if response.StatusCode != http.StatusOK {
		return Transcript{}, &Error{
			Reason: fmt.Sprintf("the transcription server answered %s", response.Status),
		}
	}

	var parsed struct {
		Text     string `json:"text"`
		Language string `json:"language"`
	}
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return Transcript{}, &Error{Reason: "the transcription server sent something unreadable"}
	}
	return Transcript{Text: cleanTranscript(parsed.Text), Language: parsed.Language}, nil
}
