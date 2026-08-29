// Package llm is the shared OpenAI-compatible client.
//
// Provider-agnostic by configuration: this speaks the OpenAI protocol, and
// base_url decides who actually answers. OpenAI, z.ai, OpenRouter, Groq, or a
// local Ollama or LM Studio server are a config change rather than a code
// change.
//
// Answers are asked for in her language explicitly. Without that instruction
// the models reply in English regardless of the voice reading them out, which
// is worse than useless when the whole point is to be understood by ear.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/exzork/mikkilens/packages/core/config"
	"github.com/exzork/mikkilens/packages/core/i18n"
)

// SummaryLimit keeps a chat summary short enough to be worth hearing.
const SummaryLimit = 600

// languageNames are how the models are told which language to answer in.
var languageNames = map[string]string{
	"id": "Bahasa Indonesia",
	"en": "English",
}

// Error is a provider failure, already phrased for speaking aloud.
type Error struct{ Reason string }

func (e *Error) Error() string { return e.Reason }

// Endpoint is one configured provider.
type Endpoint struct {
	BaseURL string
	Model   string
	APIKey  string
	Timeout time.Duration
}

// Configured reports whether this endpoint can actually be called.
func (e Endpoint) Configured() bool { return e.BaseURL != "" && e.Model != "" }

// Message is one turn in a conversation. Content is either a string or, for
// vision, a list of parts.
type Message struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

// TextPart and ImagePart make up a vision message.
type TextPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type ImagePart struct {
	Type     string   `json:"type"`
	ImageURL ImageURL `json:"image_url"`
}

type ImageURL struct {
	URL string `json:"url"`
}

// Controller runs completions against a configured provider.
type Controller struct {
	settings config.Config
	locale   *i18n.Locale
	client   *http.Client
}

// New builds a controller for the current settings.
func New(settings config.Config, locale *i18n.Locale) *Controller {
	return &Controller{
		settings: settings,
		locale:   locale,
		client:   &http.Client{},
	}
}

// Endpoint is the text provider, falling back to the vision one.
func (c *Controller) Endpoint() Endpoint {
	base, model, key := c.settings.LLMEndpoint()
	return Endpoint{
		BaseURL: base, Model: model, APIKey: key,
		Timeout: time.Duration(c.settings.Vision.TimeoutS * float64(time.Second)),
	}
}

// LanguageInstruction is appended to every system prompt.
func (c *Controller) LanguageInstruction() string {
	name, ok := languageNames[c.locale.Language]
	if !ok {
		name = c.locale.DisplayName()
	}
	return fmt.Sprintf("Answer only in %s. Keep it short and plain enough to be "+
		"read aloud by a screen reader: no markdown, no lists, no emoji.", name)
}

type completionRequest struct {
	Model     string    `json:"model"`
	Messages  []Message `json:"messages"`
	MaxTokens int       `json:"max_tokens,omitempty"`
}

type completionResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Code    any    `json:"code"`
	} `json:"error"`
}

// Complete runs one completion.
func (c *Controller) Complete(ctx context.Context, messages []Message, endpoint Endpoint, maxTokens int) (string, error) {
	if !endpoint.Configured() {
		return "", &Error{Reason: "no model endpoint is configured"}
	}
	timeout := endpoint.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	timed, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	body, err := json.Marshal(completionRequest{
		Model: endpoint.Model, Messages: messages, MaxTokens: maxTokens,
	})
	if err != nil {
		return "", &Error{Reason: err.Error()}
	}

	url := strings.TrimRight(endpoint.BaseURL, "/") + "/chat/completions"
	request, err := http.NewRequestWithContext(timed, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", &Error{Reason: err.Error()}
	}
	request.Header.Set("Content-Type", "application/json")
	if endpoint.APIKey != "" {
		request.Header.Set("Authorization", "Bearer "+endpoint.APIKey)
	}

	response, err := c.client.Do(request)
	if err != nil {
		return "", &Error{Reason: Readable(err.Error())}
	}
	defer response.Body.Close()

	payload, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return "", &Error{Reason: Readable(err.Error())}
	}

	var parsed completionResponse
	_ = json.Unmarshal(payload, &parsed)

	if response.StatusCode != http.StatusOK {
		detail := response.Status
		if parsed.Error != nil && parsed.Error.Message != "" {
			detail = fmt.Sprintf("%d %s", response.StatusCode, parsed.Error.Message)
		}
		return "", &Error{Reason: Readable(detail)}
	}
	if len(parsed.Choices) == 0 {
		return "", &Error{Reason: "the model returned nothing"}
	}
	return strings.TrimSpace(parsed.Choices[0].Message.Content), nil
}

// SummarizeChat condenses a chat backlog into something worth hearing.
func (c *Controller) SummarizeChat(ctx context.Context, messages [][2]string) (string, error) {
	if len(messages) == 0 {
		return "", nil
	}
	// Only the most recent matter, and a very long backlog would cost more than
	// the summary is worth.
	if len(messages) > 200 {
		messages = messages[len(messages)-200:]
	}

	lines := make([]string, 0, len(messages))
	for _, entry := range messages {
		lines = append(lines, entry[0]+": "+entry[1])
	}

	summary, err := c.Complete(ctx, []Message{
		{Role: "system", Content: "You summarise a live stream chat for a blind streamer. " +
			"Say what viewers are talking about, name anyone asking a direct question, " +
			"and mention nothing else. Two or three sentences at most. " +
			c.LanguageInstruction()},
		{Role: "user", Content: strings.Join(lines, "\n")},
	}, c.Endpoint(), 300)
	if err != nil {
		return "", err
	}
	return clip(summary, SummaryLimit), nil
}

// SelfTestResult is what the settings page shows after pressing Test.
type SelfTestResult struct {
	OK     bool   `json:"ok"`
	Model  string `json:"model,omitempty"`
	Answer string `json:"answer,omitempty"`
	Error  string `json:"error,omitempty"`
}

// SelfTest checks the provider answers at all.
func (c *Controller) SelfTest(ctx context.Context) SelfTestResult {
	endpoint := c.Endpoint()
	if !endpoint.Configured() {
		return SelfTestResult{Error: "no model endpoint is configured"}
	}
	reply, err := c.Complete(ctx, []Message{
		{Role: "user", Content: "Reply with the single word OK."},
	}, endpoint, 10)
	if err != nil {
		return SelfTestResult{Model: endpoint.Model, Error: err.Error()}
	}
	return SelfTestResult{OK: true, Model: endpoint.Model, Answer: reply}
}

// Readable turns a provider failure into something worth speaking aloud.
//
// The exact wording matters more than it looks: this is what she hears when a
// key expires mid-stream, and "API key ditolak" tells her what to do where a
// stack trace would not.
func Readable(text string) string {
	lowered := strings.ToLower(text)
	switch {
	case strings.Contains(lowered, "api key"),
		strings.Contains(text, "401"),
		strings.Contains(lowered, "unauthorized"),
		strings.Contains(lowered, "incorrect api key"):
		return "API key ditolak"
	case strings.Contains(lowered, "timeout"), strings.Contains(lowered, "timed out"),
		strings.Contains(lowered, "deadline exceeded"):
		return "server tidak menjawab"
	case strings.Contains(lowered, "connection"), strings.Contains(lowered, "getaddrinfo"),
		strings.Contains(lowered, "no such host"), strings.Contains(lowered, "dial tcp"):
		return "tidak bisa terhubung ke server"
	case strings.Contains(text, "404"),
		strings.Contains(lowered, "model") && strings.Contains(lowered, "not found"):
		return "model tidak ditemukan"
	}
	return clip(text, 160)
}

func clip(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}
