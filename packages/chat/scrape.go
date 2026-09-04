package chat

// Reading the page OBS reads.
//
// OBS's chat dock is not an API client. It is a browser widget pointed at
// https://www.youtube.com/live_chat?is_popout=1&v=<id>, which is why OBS shows
// chat without spending a single unit of anybody's quota. That page is public:
// it is the same thing a viewer sees, and it needs no key, no sign-in and no
// Google Cloud project.
//
// This transport reads it the way the page reads itself. The HTML carries a
// continuation token; handing that token back to the endpoint the page's own
// script calls returns the next batch of messages and the next token. Chat is
// then free, which matters more than it sounds: chat is the highest-volume
// thing MikkiLens does, and on the Data API it is what actually exhausts the
// daily allowance partway through a long stream.
//
// The cost is that none of this is a published contract. YouTube can reshape
// the page whenever it likes and nothing will announce that it has. So this is
// tried first and the Data API transports stay behind it as the fallback --
// when the shape changes, chat gets more expensive, not silent.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/exzork/mikkilens/packages/controllers/youtube"
)

const (
	// The popout form is the one OBS uses, and the lightest of the three: no
	// player, no surrounding page, just the chat document.
	liveChatPageURL = "https://www.youtube.com/live_chat?is_popout=1&v="
	liveChatNextURL = "https://www.youtube.com/youtubei/v1/live_chat/get_live_chat"

	// The page will not serve chat to something that does not look like a
	// browser, so this says what it is honestly rather than pretending to be
	// nobody at all.
	browserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
		"(KHTML, like Gecko) Chrome/125.0.0.0 Safari/537.36"

	// Floors and ceilings around the interval the server asks for. It usually
	// says about five seconds; a missing or absurd value must not turn into a
	// tight loop against YouTube's front end.
	minScrapeWait = 1 * time.Second
	maxScrapeWait = 15 * time.Second

	// The page is a few hundred kilobytes of script with the data in the
	// middle of it. Reading without a limit would let a redirect to something
	// enormous become a memory problem.
	maxPageBytes = 8 << 20
)

// scrapeTransport reads live chat from the public page.
type scrapeTransport struct {
	// client is kept across reconnects so the connection is reused. Nil means
	// the default, which is what tests use.
	client *http.Client

	// baseURL and nextURL exist so a test can point this at a local server.
	// Empty means the real thing.
	baseURL string
	nextURL string
}

func (s *scrapeTransport) Name() string { return "page" }

func (s *scrapeTransport) Run(
	ctx context.Context, target Target, deliver func([]Message), ready func(),
) error {
	if target.VideoID == "" {
		// Falling through to the Data API transports is the right answer here:
		// they work from a live chat id, which is what we do have.
		return fmt.Errorf("no video id to read the chat page for")
	}

	session, err := s.open(ctx, target.VideoID)
	if err != nil {
		return err
	}

	first := true
	for ctx.Err() == nil {
		batch, wait, err := s.next(ctx, session)
		if err != nil {
			return err
		}
		// Proof that chat is genuinely being received, which is not the same
		// as having connected: an ended stream answers a request perfectly
		// well and has nothing behind it.
		if first {
			ready()
			first = false
		}
		if len(batch) > 0 {
			deliver(batch)
		}
		if sleep(ctx, wait) {
			return ctx.Err()
		}
	}
	return ctx.Err()
}

// scrapeSession is what the page hands over: where to ask next, and the two
// values the endpoint wants alongside the continuation token.
type scrapeSession struct {
	continuation  string
	apiKey        string
	clientVersion string
}

// open fetches the chat page and reads the bootstrap out of it.
func (s *scrapeTransport) open(ctx context.Context, videoID string) (*scrapeSession, error) {
	base := s.baseURL
	if base == "" {
		base = liveChatPageURL
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base+videoID, nil)
	if err != nil {
		return nil, err
	}
	s.decorate(request)

	response, err := s.http().Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	body, err := io.ReadAll(io.LimitReader(response.Body, maxPageBytes))
	if err != nil {
		return nil, err
	}
	if response.StatusCode >= 400 {
		return nil, fmt.Errorf("the live chat page answered %d", response.StatusCode)
	}

	page := string(body)
	initial := extractJSON(page, `window["ytInitialData"] =`, `var ytInitialData =`, `ytInitialData":`)
	if len(initial) == 0 {
		return nil, fmt.Errorf("the live chat page did not carry its chat data")
	}

	var bootstrap struct {
		Contents struct {
			LiveChatRenderer *struct {
				Continuations []continuation `json:"continuations"`
			} `json:"liveChatRenderer"`
		} `json:"contents"`
	}
	if err := json.Unmarshal(initial, &bootstrap); err != nil {
		return nil, fmt.Errorf("the live chat page could not be read: %w", err)
	}
	if bootstrap.Contents.LiveChatRenderer == nil {
		// The page loads for a video with chat switched off, or for one whose
		// stream has ended, and simply has no chat in it. Retrying cannot fix
		// either, so it is reported as the thing it is.
		return nil, &youtube.ChatUnavailableError{
			Reason: "this broadcast has no live chat to read"}
	}

	token, _ := firstContinuation(bootstrap.Contents.LiveChatRenderer.Continuations)
	if token == "" {
		return nil, &youtube.ChatUnavailableError{
			Reason: "this broadcast's live chat has ended"}
	}

	session := &scrapeSession{
		continuation:  token,
		apiKey:        between(page, `"INNERTUBE_API_KEY":"`, `"`),
		clientVersion: between(page, `"INNERTUBE_CLIENT_VERSION":"`, `"`),
	}
	if session.clientVersion == "" {
		// The endpoint refuses a request with no client version at all. A
		// plausible one keeps working when the page stops volunteering it,
		// which is a better failure than none at all.
		session.clientVersion = "2.20250101.00.00"
	}
	return session, nil
}

// next asks for the batch after the current continuation.
func (s *scrapeTransport) next(ctx context.Context, session *scrapeSession) ([]Message, time.Duration, error) {
	payload, err := json.Marshal(map[string]any{
		"context": map[string]any{
			"client": map[string]any{
				"clientName":    "WEB",
				"clientVersion": session.clientVersion,
			},
		},
		"continuation": session.continuation,
	})
	if err != nil {
		return nil, 0, err
	}

	endpoint := s.nextURL
	if endpoint == "" {
		endpoint = liveChatNextURL
	}
	if session.apiKey != "" {
		endpoint += "?key=" + session.apiKey + "&prettyPrint=false"
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, 0, err
	}
	s.decorate(request)
	request.Header.Set("Content-Type", "application/json")

	response, err := s.http().Do(request)
	if err != nil {
		return nil, 0, err
	}
	defer response.Body.Close()

	body, err := io.ReadAll(io.LimitReader(response.Body, maxPageBytes))
	if err != nil {
		return nil, 0, err
	}
	switch {
	case response.StatusCode == http.StatusTooManyRequests:
		return nil, 0, &youtube.RateLimitedError{Reason: "YouTube is asking us to slow down"}
	case response.StatusCode == http.StatusNotFound:
		return nil, 0, &youtube.ChatUnavailableError{Reason: "this live chat is no longer there"}
	case response.StatusCode >= 400:
		return nil, 0, fmt.Errorf("the live chat endpoint answered %d", response.StatusCode)
	}

	var answer chatAnswer
	if err := json.Unmarshal(body, &answer); err != nil {
		return nil, 0, fmt.Errorf("the live chat answer could not be read: %w", err)
	}

	live := answer.ContinuationContents.LiveChatContinuation
	token, timeout := firstContinuation(live.Continuations)
	if token == "" {
		// No token back means there is nothing further to follow: the stream
		// ended, or chat closed while we were reading it.
		return nil, 0, &youtube.ChatUnavailableError{Reason: "this live chat has ended"}
	}
	session.continuation = token

	messages := make([]Message, 0, len(live.Actions))
	for _, act := range live.Actions {
		if act.AddChatItemAction == nil {
			continue
		}
		if parsed, ok := act.AddChatItemAction.Item.message(); ok {
			messages = append(messages, parsed)
		}
	}

	wait := time.Duration(timeout) * time.Millisecond
	switch {
	case wait < minScrapeWait:
		wait = minScrapeWait
	case wait > maxScrapeWait:
		wait = maxScrapeWait
	}
	return messages, wait, nil
}

func (s *scrapeTransport) http() *http.Client {
	if s.client != nil {
		return s.client
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// decorate makes the request look like the browser the page expects.
func (s *scrapeTransport) decorate(request *http.Request) {
	request.Header.Set("User-Agent", browserAgent)
	request.Header.Set("Accept-Language", "en-US,en;q=0.9")
	// Without a settled consent choice some regions serve a consent wall
	// instead of the page, and the chat data is simply not in it.
	request.Header.Set("Cookie", "CONSENT=YES+cb")
}

// -- the shape of the answer --------------------------------------------------

type chatAnswer struct {
	ContinuationContents struct {
		LiveChatContinuation struct {
			Continuations []continuation `json:"continuations"`
			Actions       []chatAction   `json:"actions"`
		} `json:"liveChatContinuation"`
	} `json:"continuationContents"`
}

// continuation is one of several shapes carrying the same two fields. Which
// one arrives depends on whether the chat is idle, busy, or just opened, so
// all of them are read and the first with a token wins.
type continuation struct {
	Invalidation *continuationData `json:"invalidationContinuationData"`
	Timed        *continuationData `json:"timedContinuationData"`
	Reload       *continuationData `json:"reloadContinuationData"`
	Replay       *continuationData `json:"liveChatReplayContinuationData"`
}

type continuationData struct {
	Continuation string `json:"continuation"`
	TimeoutMs    int    `json:"timeoutMs"`
}

func firstContinuation(items []continuation) (string, int) {
	for _, item := range items {
		for _, data := range []*continuationData{
			item.Invalidation, item.Timed, item.Reload, item.Replay,
		} {
			if data != nil && data.Continuation != "" {
				return data.Continuation, data.TimeoutMs
			}
		}
	}
	return "", 0
}

type chatAction struct {
	AddChatItemAction *struct {
		Item chatItem `json:"item"`
	} `json:"addChatItemAction"`
}

// chatItem is a union: exactly one of these is present, and which one is what
// distinguishes an ordinary message from a super chat or a membership.
type chatItem struct {
	Text       *chatRenderer `json:"liveChatTextMessageRenderer"`
	Paid       *chatRenderer `json:"liveChatPaidMessageRenderer"`
	Sticker    *chatRenderer `json:"liveChatPaidStickerRenderer"`
	Membership *chatRenderer `json:"liveChatMembershipItemRenderer"`
	Gift       *chatRenderer `json:"liveChatSponsorshipsGiftPurchaseAnnouncementRenderer"`
}

type chatRenderer struct {
	ID            string      `json:"id"`
	TimestampUsec string      `json:"timestampUsec"`
	AuthorName    *simpleText `json:"authorName"`
	Message       *textRuns   `json:"message"`
	// Membership announcements put their words here instead of in message:
	// "Welcome to level 2!" and the like.
	HeaderSubtext     *textRuns     `json:"headerSubtext"`
	PurchaseAmount    *simpleText   `json:"purchaseAmountText"`
	StickerAccessible *a11yLabel    `json:"stickerAccessibility"`
	AuthorBadges      []authorBadge `json:"authorBadges"`
}

type simpleText struct {
	SimpleText string `json:"simpleText"`
}

type a11yLabel struct {
	AccessibilityData struct {
		Label string `json:"label"`
	} `json:"accessibilityData"`
}

type textRuns struct {
	SimpleText string `json:"simpleText"`
	Runs       []struct {
		Text  string `json:"text"`
		Emoji *struct {
			EmojiID       string   `json:"emojiId"`
			Shortcuts     []string `json:"shortcuts"`
			IsCustomEmoji bool     `json:"isCustomEmoji"`
		} `json:"emoji"`
	} `json:"runs"`
}

// text flattens the runs into something speakable.
//
// A channel's own emotes have no unicode character to read, only a name like
// :_wave:, so the name is used with its decoration stripped. Standard emoji
// carry the character itself in emojiId, and are kept: whether they are read
// out is the reader's decision, not this one's.
func (t *textRuns) text() string {
	if t == nil {
		return ""
	}
	if len(t.Runs) == 0 {
		return strings.TrimSpace(t.SimpleText)
	}

	var built strings.Builder
	for _, run := range t.Runs {
		switch {
		case run.Emoji == nil:
			built.WriteString(run.Text)
		case !run.Emoji.IsCustomEmoji && run.Emoji.EmojiID != "":
			built.WriteString(run.Emoji.EmojiID)
		case len(run.Emoji.Shortcuts) > 0:
			built.WriteString(strings.Trim(run.Emoji.Shortcuts[0], ":_"))
		}
	}
	return strings.TrimSpace(built.String())
}

type authorBadge struct {
	Renderer struct {
		Tooltip string `json:"tooltip"`
		Icon    *struct {
			IconType string `json:"iconType"`
		} `json:"icon"`
		// A member badge is the channel's own image rather than one of
		// YouTube's icons, which is how membership is told apart from the
		// owner and moderator badges.
		CustomThumbnail json.RawMessage `json:"customThumbnail"`
	} `json:"liveChatAuthorBadgeRenderer"`
}

// message turns one item into a Message, or reports that there was nothing
// readable in it.
func (i chatItem) message() (Message, bool) {
	var (
		renderer    *chatRenderer
		isSuperchat bool
		isMember    bool
	)
	switch {
	case i.Text != nil:
		renderer = i.Text
	case i.Paid != nil:
		renderer, isSuperchat = i.Paid, true
	case i.Sticker != nil:
		renderer, isSuperchat = i.Sticker, true
	case i.Membership != nil:
		renderer, isMember = i.Membership, true
	case i.Gift != nil:
		renderer, isMember = i.Gift, true
	default:
		// Deletions, pinned banners, poll results and the rest. Ignoring them
		// is deliberate: they are not things a viewer said.
		return Message{}, false
	}

	text := renderer.Message.text()
	if text == "" {
		text = renderer.HeaderSubtext.text()
	}
	if text == "" && renderer.StickerAccessible != nil {
		text = strings.TrimSpace(renderer.StickerAccessible.AccessibilityData.Label)
	}

	author := ""
	if renderer.AuthorName != nil {
		author = strings.TrimSpace(renderer.AuthorName.SimpleText)
	}
	amount := ""
	if renderer.PurchaseAmount != nil {
		amount = strings.TrimSpace(renderer.PurchaseAmount.SimpleText)
	}

	isOwner, isModerator := false, false
	for _, badge := range renderer.AuthorBadges {
		switch {
		case badge.Renderer.Icon == nil:
			if len(badge.Renderer.CustomThumbnail) > 0 {
				isMember = true
			}
		case badge.Renderer.Icon.IconType == "OWNER":
			isOwner = true
		case badge.Renderer.Icon.IconType == "MODERATOR":
			isModerator = true
		}
	}

	// A paid message with no words is still worth announcing -- somebody sent
	// money -- so an empty text is only fatal for an ordinary one.
	if text == "" && !isSuperchat && !isMember {
		return Message{}, false
	}

	return Message{
		ID:          renderer.ID,
		Author:      author,
		Text:        text,
		PublishedAt: publishedAt(renderer.TimestampUsec),
		ReceivedAt:  float64(time.Now().UnixNano()) / 1e9,
		IsSuperchat: isSuperchat,
		IsMember:    isMember,
		Amount:      amount,
		IsOwner:     isOwner,
		IsModerator: isModerator,
	}, true
}

// publishedAt renders the page's microsecond stamp the way the Data API
// renders its own.
//
// The width is fixed rather than left to RFC3339Nano, which trims trailing
// zeros. The read cursor compares these as text, and a trimmed fraction sorts
// wrongly against an untrimmed one -- which would show up as a handful of
// messages being read aloud twice after a restart.
func publishedAt(usec string) string {
	micros, err := strconv.ParseInt(usec, 10, 64)
	if err != nil || micros <= 0 {
		return time.Now().UTC().Format(stampLayout)
	}
	return time.UnixMicro(micros).UTC().Format(stampLayout)
}

const stampLayout = "2006-01-02T15:04:05.000000Z07:00"

// -- pulling json out of a page of script -------------------------------------

// extractJSON finds the first of several markers and returns the JSON object
// that follows it, matched brace by brace.
//
// Cutting at the end of the line would be simpler and wrong: the data is one
// line, minified, and the line ends long after the object does.
func extractJSON(page string, markers ...string) []byte {
	for _, marker := range markers {
		at := strings.Index(page, marker)
		if at < 0 {
			continue
		}
		start := strings.Index(page[at+len(marker):], "{")
		if start < 0 {
			continue
		}
		start += at + len(marker)
		if end := matchBrace(page, start); end > start {
			return []byte(page[start:end])
		}
	}
	return nil
}

// matchBrace returns the index just past the object opening at start, honouring
// strings and their escapes so that a brace inside a chat message -- which is
// an ordinary thing for someone to type -- does not end the object early.
func matchBrace(page string, start int) int {
	depth, inString, escaped := 0, false, false
	for index := start; index < len(page); index++ {
		character := page[index]
		switch {
		case escaped:
			escaped = false
		case character == '\\' && inString:
			escaped = true
		case character == '"':
			inString = !inString
		case inString:
			// Nothing structural inside a string.
		case character == '{':
			depth++
		case character == '}':
			if depth--; depth == 0 {
				return index + 1
			}
		}
	}
	return -1
}

// between returns what sits between two markers, or "".
func between(page, prefix, suffix string) string {
	at := strings.Index(page, prefix)
	if at < 0 {
		return ""
	}
	rest := page[at+len(prefix):]
	end := strings.Index(rest, suffix)
	if end < 0 {
		return ""
	}
	return rest[:end]
}
