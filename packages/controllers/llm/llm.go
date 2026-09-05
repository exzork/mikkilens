// Package llm is the shared OpenAI-compatible client.
//
// Provider-agnostic by configuration: this speaks the OpenAI protocol, and
// base_url decides who actually answers. OpenAI, z.ai, OpenRouter, Groq, or a
// local Ollama or LM Studio server are a config change rather than a code
// change -- and running a model on her own machine stays available that way,
// as one line of config, rather than as a downloader and a child process
// MikkiLens has to keep alive.
//
// There is one endpoint and one model behind it, asked everything: what a
// misheard command meant, what chat has been talking about, and what is on
// screen. So the model has to be one that can see.
//
// Answers are asked for in her language explicitly. Without that instruction
// the models reply in English regardless of the voice reading them out, which
// is worse than useless when the whole point is to be understood by ear.
package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
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

// Endpoint is the configured provider.
func (c *Controller) Endpoint() Endpoint {
	base, model, key := c.settings.ModelEndpoint()
	return Endpoint{
		BaseURL: base, Model: model, APIKey: key,
		Timeout: time.Duration(c.settings.Model.TimeoutS * float64(time.Second)),
	}
}

// LanguageInstruction is appended to every system prompt.
func (c *Controller) LanguageInstruction() string {
	name, ok := languageNames[c.locale.Language]
	if !ok {
		name = c.locale.DisplayName()
	}
	return fmt.Sprintf("Answer only in %s. Keep it short and plain enough to be "+
		"read aloud: no markdown, no lists, no emoji.", name)
}

type completionRequest struct {
	Model     string    `json:"model"`
	Messages  []Message `json:"messages"`
	MaxTokens int       `json:"max_tokens,omitempty"`
	Tools     []Tool    `json:"tools,omitempty"`
	// ToolChoice is "auto" whenever tools are offered: the model must stay
	// free to call nothing, because "none of these" is a real answer here.
	ToolChoice string `json:"tool_choice,omitempty"`
	Stream     bool   `json:"stream,omitempty"`
}

// Tool is one function the model may call, in the shape every
// OpenAI-compatible provider expects.
type Tool struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

// ToolFunction names a tool and describes the arguments it takes.
type ToolFunction struct {
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	Parameters  ToolSchema `json:"parameters"`
}

// ToolSchema is the JSON Schema for one tool's arguments.
//
// additionalProperties is false so a model cannot invent an argument that
// nothing downstream reads; an unknown slot is a sign it misunderstood, and
// silently dropping it would hide that.
type ToolSchema struct {
	Type                 string                  `json:"type"`
	Properties           map[string]ToolProperty `json:"properties"`
	Required             []string                `json:"required,omitempty"`
	AdditionalProperties bool                    `json:"additionalProperties"`
}

// ToolProperty is one argument.
type ToolProperty struct {
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
}

// ToolCall is one call the model asked for, with its arguments already decoded.
type ToolCall struct {
	Name      string
	Arguments map[string]string
}

type completionResponse struct {
	Choices []struct {
		Message struct {
			Content   string `json:"content"`
			ToolCalls []struct {
				Function struct {
					Name string `json:"name"`
					// Arguments is JSON, but delivered as a string.
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Code    any    `json:"code"`
	} `json:"error"`
}

// Complete runs one completion.
func (c *Controller) Complete(ctx context.Context, messages []Message, endpoint Endpoint, maxTokens int) (string, error) {
	content, _, err := c.complete(ctx, messages, endpoint, maxTokens, nil)
	return content, err
}

// CompleteTools runs one completion with tools on the table.
//
// Returns whatever the model called and whatever it said. No call is not a
// failure: with tool_choice "auto" it is how the model declines, which is the
// answer this is most interested in getting honestly.
func (c *Controller) CompleteTools(
	ctx context.Context, messages []Message, endpoint Endpoint, maxTokens int, tools []Tool,
) (string, []ToolCall, error) {
	return c.complete(ctx, messages, endpoint, maxTokens, tools)
}

func (c *Controller) complete(
	ctx context.Context, messages []Message, endpoint Endpoint, maxTokens int, tools []Tool,
) (string, []ToolCall, error) {
	if !endpoint.Configured() {
		return "", nil, &Error{Reason: "no model endpoint is configured"}
	}
	timeout := endpoint.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	timed, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	asked := completionRequest{
		Model: endpoint.Model, Messages: messages, MaxTokens: maxTokens,
	}
	if len(tools) > 0 {
		asked.Tools = tools
		asked.ToolChoice = "auto"
	}
	body, err := json.Marshal(asked)
	if err != nil {
		return "", nil, &Error{Reason: err.Error()}
	}

	url := strings.TrimRight(endpoint.BaseURL, "/") + "/chat/completions"
	request, err := http.NewRequestWithContext(timed, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", nil, &Error{Reason: err.Error()}
	}
	request.Header.Set("Content-Type", "application/json")
	if endpoint.APIKey != "" {
		request.Header.Set("Authorization", "Bearer "+endpoint.APIKey)
	}

	response, err := c.client.Do(request)
	if err != nil {
		return "", nil, &Error{Reason: Readable(err.Error())}
	}
	defer response.Body.Close()

	payload, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return "", nil, &Error{Reason: Readable(err.Error())}
	}

	var parsed completionResponse
	_ = json.Unmarshal(payload, &parsed)

	if response.StatusCode != http.StatusOK {
		detail := response.Status
		if parsed.Error != nil && parsed.Error.Message != "" {
			detail = fmt.Sprintf("%d %s", response.StatusCode, parsed.Error.Message)
		}
		return "", nil, &Error{Reason: Readable(detail)}
	}
	if len(parsed.Choices) == 0 {
		return "", nil, &Error{Reason: "the model returned nothing"}
	}

	message := parsed.Choices[0].Message
	calls := make([]ToolCall, 0, len(message.ToolCalls))
	for _, raw := range message.ToolCalls {
		if name := strings.TrimSpace(raw.Function.Name); name != "" {
			calls = append(calls, ToolCall{
				Name:      name,
				Arguments: decodeArguments(raw.Function.Arguments),
			})
		}
	}
	return strings.TrimSpace(message.Content), calls, nil
}

// decodeArguments reads a tool call's arguments.
//
// Everything is turned into a string because that is what a command slot is:
// a scene name, a title, a question. A model that answers a number rather than
// a string is not wrong enough to throw the whole call away over.
func decodeArguments(raw string) map[string]string {
	arguments := map[string]string{}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return arguments
	}

	var decoded map[string]any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return arguments
	}
	for name, value := range decoded {
		text := ""
		switch typed := value.(type) {
		case string:
			text = typed
		case nil:
			continue
		default:
			text = fmt.Sprint(typed)
		}
		if text = strings.TrimSpace(text); text != "" {
			arguments[strings.TrimSpace(name)] = text
		}
	}
	return arguments
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
		{Role: "system", Content: "You summarise a live stream chat for a streamer who is listening to it " +
			"rather than reading it. " +
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

// -- streaming ------------------------------------------------------------

// Speaking before the model has finished thinking.
//
// A model composing two sentences takes a few seconds, and waiting for the
// last word before saying the first is the whole of that wait spent in
// silence. Silence is the one thing this application treats as a fault: it is
// indistinguishable from a command that did nothing.
//
// So the reply is read as it arrives and handed over a sentence at a time. The
// first one is usually the answer -- models put it first when asked to -- so
// she hears it while the rest is still being written.

// sentenceEnd is where it is safe to stop and speak.
//
// Whitespace after the punctuation is required, and end-of-string is not
// accepted. That looks like an oversight and is the opposite: this runs over a
// buffer that is still being filled, so "$" means "as much as has arrived so
// far", not "the end of the sentence". A chunk that happened to end just after
// a full stop would split there -- and the reply "pukul 09.41" arrives in
// chunks, so it came out as "pukul 09." and then "41", read aloud as two
// sentences and two wrong numbers.
//
// Whatever is left when the stream ends is spoken by the caller, so nothing is
// lost by waiting for proof that the sentence is really over.
var sentenceEnd = regexp.MustCompile(`[.!?]["')\]]*\s`)

// splitSentence returns the first complete sentence in buffered text and what
// is left, or "" when nothing is finished yet.
func splitSentence(buffered string) (sentence, rest string) {
	for _, found := range sentenceEnd.FindAllStringIndex(buffered, -1) {
		end := found[1]
		candidate := strings.TrimSpace(buffered[:end])
		if candidate == "" {
			continue
		}
		// A decimal point has digits on both sides; a full stop does not.
		if dot := found[0]; dot > 0 && dot+1 < len(buffered) &&
			isDigit(buffered[dot-1]) && isDigit(buffered[dot+1]) {
			continue
		}
		// Too short to be a sentence is usually an abbreviation or a stray
		// initial; waiting for more is the safer read.
		if len([]rune(candidate)) < 12 && end < len(buffered) {
			continue
		}
		return candidate, buffered[end:]
	}
	return "", buffered
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// CompleteStream runs a completion and hands over each sentence as it lands.
//
// onSentence is called on this goroutine, in order, once per finished
// sentence. The whole reply is returned as well, for anything that wants to
// record what was said.
func (c *Controller) CompleteStream(
	ctx context.Context, messages []Message, endpoint Endpoint, maxTokens int,
	onSentence func(string),
) (string, error) {
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
		Model: endpoint.Model, Messages: messages, MaxTokens: maxTokens, Stream: true,
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
	request.Header.Set("Accept", "text/event-stream")
	if endpoint.APIKey != "" {
		request.Header.Set("Authorization", "Bearer "+endpoint.APIKey)
	}

	response, err := c.client.Do(request)
	if err != nil {
		return "", &Error{Reason: Readable(err.Error())}
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		payload, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		var parsed completionResponse
		_ = json.Unmarshal(payload, &parsed)
		detail := response.Status
		if parsed.Error != nil && parsed.Error.Message != "" {
			detail = fmt.Sprintf("%d %s", response.StatusCode, parsed.Error.Message)
		}
		return "", &Error{Reason: Readable(detail)}
	}

	var whole strings.Builder
	buffered := ""
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}

		var chunk streamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			// One malformed frame is not worth abandoning an answer already
			// half spoken.
			continue
		}
		for _, choice := range chunk.Choices {
			piece := choice.Delta.Content
			if piece == "" {
				continue
			}
			whole.WriteString(piece)
			buffered += piece
			for {
				sentence, rest := splitSentence(buffered)
				if sentence == "" {
					break
				}
				buffered = rest
				if onSentence != nil {
					onSentence(sentence)
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		// Whatever arrived has already been spoken, so this is reported rather
		// than thrown away.
		return strings.TrimSpace(whole.String()), &Error{Reason: Readable(err.Error())}
	}

	// Whatever is left had no full stop on the end. It is still an answer.
	if tail := strings.TrimSpace(buffered); tail != "" && onSentence != nil {
		onSentence(tail)
	}
	return strings.TrimSpace(whole.String()), nil
}

type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
}
