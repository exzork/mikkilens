package chat

import (
	"bufio"
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

func (s *streamTransport) Run(
	ctx context.Context, target Target, deliver func([]Message), ready func(),
) error {
	if target.LiveChatID == "" {
		return &youtube.NotAuthenticatedError{Reason: "streaming chat needs a live chat id, " +
			"which needs an API key or a sign-in"}
	}

	query := url.Values{}
	query.Set("liveChatId", target.LiveChatID)
	query.Set("part", "id,snippet,authorDetails")

	client := &http.Client{Timeout: 0} // a streaming response has no deadline
	backoff := time.Duration(0)

	for ctx.Err() == nil {
		if backoff > 0 && sleep(ctx, backoff) {
			return ctx.Err()
		}

		started := time.Now()
		nextPage, err := s.connect(ctx, client, query, deliver, ready)
		if err != nil {
			return err
		}
		if nextPage != "" {
			query.Set("pageToken", nextPage)
		}

		// The server ends the response periodically by design and reconnecting
		// is normal, so a connection that lasted a while resumes at once. One
		// that died immediately must not: reconnecting in a tight loop would
		// turn a transient fault into a spin against Google's front end, and
		// the failure would look like chat simply going quiet.
		if time.Since(started) > healthyStream {
			backoff = 0
		} else {
			backoff = nextBackoff(backoff)
		}
	}
	return ctx.Err()
}

// healthyStream is how long a connection must last to count as working.
const healthyStream = 30 * time.Second

// maxStreamBackoff caps the wait. Longer than this and a recovering stream
// would stay silent well after YouTube came back.
const maxStreamBackoff = 30 * time.Second

func nextBackoff(previous time.Duration) time.Duration {
	if previous <= 0 {
		return time.Second
	}
	return min(previous*2, maxStreamBackoff)
}

// connect opens one streaming response and reads it to its end.
func (s *streamTransport) connect(
	ctx context.Context,
	client *http.Client,
	query url.Values,
	deliver func([]Message),
	ready func(),
) (string, error) {
	request, err := http.NewRequestWithContext(
		ctx, http.MethodGet, streamURL+"?"+query.Encode(), nil)
	if err != nil {
		return "", err
	}
	// Authorized per connection rather than once per stream: an access token
	// lasts about an hour, and a stream can run far longer than that. Doing it
	// once was a bug that would drop chat mid-broadcast.
	if err := s.youtube.AuthorizeStream(request); err != nil {
		return "", err
	}

	response, err := client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()

	// Counted on connect, not per message. Streaming is charged by the
	// connection, which is exactly why it is preferred over polling.
	s.youtube.Quota.Spend("liveChatMessages.stream")

	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return "", s.youtube.ClassifyHTTP(response.StatusCode, body,
			fmt.Errorf("streamList returned %s: %s",
				response.Status, clip(string(body), 200)))
	}

	// Announced here, not before the request: a 200 is the first moment chat
	// is genuinely working. Saying so on the attempt instead meant announcing
	// a connection that was about to fail, then announcing the failure, over
	// and over.
	ready()
	return s.consume(ctx, response.Body, deliver)
}

// consume reads the streaming response.
//
// The endpoint answers with a JSON *array* written a piece at a time -- "["
// and then one document per batch of messages, the closing "]" arriving only
// when the server ends the response. Decoding that as a single value would
// wait for an array that has not finished, which is to say forever; decoding
// the elements one at a time is what makes it a stream rather than a very
// slow download.
//
// A bare sequence of objects is accepted too, so a change in framing degrades
// into the older behaviour rather than into silence.
func (s *streamTransport) consume(ctx context.Context, body io.Reader, deliver func([]Message)) (string, error) {
	reader := bufio.NewReader(body)
	framed, err := peekArrayOpening(reader)
	if err != nil {
		return "", err
	}

	decoder := json.NewDecoder(reader)
	if framed {
		// The decoder has to read the "[" itself. It then tracks that it is
		// inside an array, which is what makes More() work and what lets
		// Decode skip the commas between elements -- swallowing the bracket
		// beforehand leaves it choking on the first comma instead.
		if _, err := decoder.Token(); err != nil {
			return "", err
		}
	}
	nextPage := ""

	for {
		if ctx.Err() != nil {
			return nextPage, ctx.Err()
		}
		if framed && !decoder.More() {
			return nextPage, nil // the array closed; reconnect and carry on
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

// peekArrayOpening reports whether the body starts an array, without
// consuming the bracket.
//
// Looking rather than reading matters: a json.Decoder cannot put a token
// back, so taking the "[" only to discover it was a "{" would corrupt
// everything after it. Leading whitespace is consumed, which JSON ignores.
func peekArrayOpening(reader *bufio.Reader) (bool, error) {
	for {
		next, err := reader.Peek(1)
		if err != nil {
			return false, err
		}
		switch next[0] {
		case ' ', '\t', '\r', '\n':
			if _, err := reader.ReadByte(); err != nil {
				return false, err
			}
		case '[':
			return true, nil
		default:
			return false, nil
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

func (p *pollingTransport) Run(
	ctx context.Context, target Target, deliver func([]Message), ready func(),
) error {
	if target.LiveChatID == "" {
		return &youtube.NotAuthenticatedError{Reason: "polling chat needs a live chat id, " +
			"which needs an API key or a sign-in"}
	}

	pageToken := ""

	for ctx.Err() == nil {
		response, err := p.youtube.ListChatMessages(ctx, target.LiveChatID, pageToken)
		if err != nil {
			return err
		}
		// The first answer that comes back is the proof chat works.
		ready()

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
