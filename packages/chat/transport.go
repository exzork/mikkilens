package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	yt "google.golang.org/api/youtube/v3"

	"github.com/exzork/mikkilens/packages/controllers/youtube"
)

// streamURL is the server-streaming endpoint. Messages are pushed rather than
// polled for, which is what keeps a four hour stream inside the daily quota.
const streamURL = "https://youtube.googleapis.com/youtube/v3/liveChat/messages/stream"

// streamTransport holds a streaming connection open.
type streamTransport struct {
	youtube *youtube.Controller
}

func (s *streamTransport) Name() string { return "stream" }

func (s *streamTransport) Run(ctx context.Context, liveChatID string, deliver func([]Message)) error {
	token, err := s.youtube.Token()
	if err != nil {
		return err
	}

	query := url.Values{}
	query.Set("liveChatId", liveChatID)
	query.Set("part", "id,snippet,authorDetails")
	query.Set("maxResults", "500")

	client := &http.Client{Timeout: 0} // a streaming response has no deadline

	for ctx.Err() == nil {
		request, err := http.NewRequestWithContext(
			ctx, http.MethodGet, streamURL+"?"+query.Encode(), nil)
		if err != nil {
			return err
		}
		request.Header.Set("Authorization", "Bearer "+token.AccessToken)

		response, err := client.Do(request)
		if err != nil {
			return err
		}
		if response.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
			response.Body.Close()
			return fmt.Errorf("streamList returned %s: %s", response.Status, clip(string(body), 200))
		}

		nextPage, err := s.consume(ctx, response.Body, deliver)
		response.Body.Close()
		if err != nil {
			return err
		}
		if nextPage != "" {
			query.Set("pageToken", nextPage)
		}
	}
	return ctx.Err()
}

// consume reads a chunked stream of JSON values.
//
// The response is a sequence of JSON documents rather than one document, split
// at arbitrary byte offsets, so it is decoded incrementally.
func (s *streamTransport) consume(ctx context.Context, body io.Reader, deliver func([]Message)) (string, error) {
	decoder := json.NewDecoder(body)
	nextPage := ""

	for {
		if ctx.Err() != nil {
			return nextPage, ctx.Err()
		}

		var payload struct {
			Items         []*yt.LiveChatMessage `json:"items"`
			NextPageToken string                `json:"nextPageToken"`
		}
		if err := decoder.Decode(&payload); err != nil {
			if err == io.EOF {
				return nextPage, nil // the server closed; reconnect and carry on
			}
			return nextPage, err
		}

		messages := make([]Message, 0, len(payload.Items))
		for _, item := range payload.Items {
			if parsed, ok := ParseMessage(item); ok {
				messages = append(messages, parsed)
			}
		}
		if len(messages) > 0 {
			deliver(messages)
		}
		if payload.NextPageToken != "" {
			nextPage = payload.NextPageToken
		}
	}
}

// pollingTransport is the fallback: repeated list calls, paced by the API's own
// hint about how often to ask.
type pollingTransport struct {
	youtube        *youtube.Controller
	onQuotaWarning func(percent int)
	warned         bool
}

// MinPollInterval is the floor. Polling faster than this would burn the daily
// quota on a long stream, which would stop chat dead partway through.
const MinPollInterval = 5 * time.Second

func (p *pollingTransport) Name() string { return "poll" }

func (p *pollingTransport) Run(ctx context.Context, liveChatID string, deliver func([]Message)) error {
	pageToken := ""

	for ctx.Err() == nil {
		response, err := p.youtube.ListChatMessages(ctx, liveChatID, pageToken)
		if err != nil {
			return err
		}

		messages := make([]Message, 0, len(response.Items))
		for _, item := range response.Items {
			if parsed, ok := ParseMessage(item); ok {
				messages = append(messages, parsed)
			}
		}
		if len(messages) > 0 {
			deliver(messages)
		}
		pageToken = response.NextPageToken

		interval := time.Duration(response.PollingIntervalMillis) * time.Millisecond
		if interval < MinPollInterval {
			interval = MinPollInterval
		}
		// Slow down as the budget runs out, rather than stopping dead.
		if p.youtube.Quota.ShouldWarn() {
			interval *= 3
			if !p.warned && p.onQuotaWarning != nil {
				p.warned = true
				p.onQuotaWarning(p.youtube.Quota.Percent())
			}
		}
		if sleep(ctx, interval) {
			return ctx.Err()
		}
	}
	return ctx.Err()
}

func clip(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[:limit]
}
