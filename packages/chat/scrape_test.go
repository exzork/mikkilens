package chat

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/exzork/mikkilens/packages/controllers/youtube"
)

// The page is not a published contract, so these tests pin the shape this
// transport actually depends on. When YouTube changes it they are what turns
// "chat went quiet" into a named failure.

// page builds a live chat page the way the real one is built: the data buried
// in a line of minified script, not on a line of its own.
func page(continuation string) string {
	return `<!DOCTYPE html><html><head><script>` +
		`var meta = {"INNERTUBE_API_KEY":"AIzaTest","INNERTUBE_CLIENT_VERSION":"2.20250101.00.00"};` +
		`window["ytInitialData"] = {"contents":{"liveChatRenderer":{"continuations":` +
		`[{"invalidationContinuationData":{"timeoutMs":5000,"continuation":"` + continuation + `"}}]}}};` +
		`var after = {"unrelated":true};</script></head><body></body></html>`
}

// noChatPage is what a video with chat switched off, or an ended stream,
// actually serves: a valid page with no chat renderer in it.
const noChatPage = `<html><script>window["ytInitialData"] = ` +
	`{"contents":{"twoColumnWatchNextResults":{}}};</script></html>`

func chatItemJSON(renderer, body string) string {
	return `{"addChatItemAction":{"item":{"` + renderer + `":` + body + `}}}`
}

// serve stands up a page and an endpoint, and returns a transport pointed at
// them plus a count of how many times the endpoint was asked.
func serve(t *testing.T, pageBody string, answers ...string) (*scrapeTransport, *int) {
	t.Helper()

	asked := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/live_chat", func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(pageBody))
	})
	mux.HandleFunc("/next", func(writer http.ResponseWriter, request *http.Request) {
		var sent struct {
			Continuation string `json:"continuation"`
			Context      struct {
				Client struct {
					ClientVersion string `json:"clientVersion"`
				} `json:"client"`
			} `json:"context"`
		}
		if err := json.NewDecoder(request.Body).Decode(&sent); err != nil {
			t.Errorf("the endpoint was sent something unreadable: %v", err)
		}
		if sent.Continuation == "" {
			t.Error("the continuation token must be sent back")
		}
		if sent.Context.Client.ClientVersion == "" {
			t.Error("the endpoint refuses a request with no client version")
		}
		index := min(asked, len(answers)-1)
		asked++
		_, _ = writer.Write([]byte(answers[index]))
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	return &scrapeTransport{
		client:  server.Client(),
		baseURL: server.URL + "/live_chat?v=",
		nextURL: server.URL + "/next",
	}, &asked
}

// answer wraps actions in the envelope the endpoint really returns.
func answer(timeoutMs int, actions ...string) string {
	return `{"continuationContents":{"liveChatContinuation":{"continuations":` +
		`[{"invalidationContinuationData":{"timeoutMs":` +
		strconv.Itoa(timeoutMs) + `,"continuation":"next-token"}}],"actions":[` +
		strings.Join(actions, ",") + `]}}}`
}

// collectScraped runs the transport until it has delivered a batch, then stops it the
// way the ingest loop would.
func collectScraped(t *testing.T, transport *scrapeTransport) []Message {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	got := make(chan []Message, 1)
	go func() {
		_ = transport.Run(ctx, Target{VideoID: "abcdefghijk"}, func(batch []Message) {
			select {
			case got <- batch:
			default:
			}
		}, func() {})
	}()

	select {
	case batch := <-got:
		return batch
	case <-ctx.Done():
		t.Fatal("no messages arrived")
		return nil
	}
}

func TestAnOrdinaryMessageIsReadFromThePage(t *testing.T) {
	transport, _ := serve(t, page("first-token"), answer(5000, chatItemJSON(
		"liveChatTextMessageRenderer",
		`{"id":"msg-1","timestampUsec":"1756600000000000",`+
			`"authorName":{"simpleText":"Rina"},`+
			`"message":{"runs":[{"text":"halo mikki"}]}}`)))

	messages := collectScraped(t, transport)
	if len(messages) != 1 {
		t.Fatalf("got %d messages, want 1", len(messages))
	}
	message := messages[0]
	if message.ID != "msg-1" || message.Author != "Rina" || message.Text != "halo mikki" {
		t.Errorf("message = %+v", message)
	}
	if message.IsSuperchat || message.IsMember || message.IsOwner || message.IsModerator {
		t.Errorf("an ordinary message must be flagged as nothing special: %+v", message)
	}
	if message.PublishedAt == "" {
		t.Error("the read cursor needs a timestamp, or restarts re-read everything")
	}
}

// A super chat has to survive the trip, because it is the one message that
// jumps the queue and gets read out ahead of everything else.
func TestASuperChatKeepsItsAmount(t *testing.T) {
	transport, _ := serve(t, page("first-token"), answer(5000, chatItemJSON(
		"liveChatPaidMessageRenderer",
		`{"id":"paid-1","timestampUsec":"1756600000000000",`+
			`"authorName":{"simpleText":"Budi"},`+
			`"purchaseAmountText":{"simpleText":"Rp50.000"},`+
			`"message":{"runs":[{"text":"semangat!"}]}}`)))

	messages := collectScraped(t, transport)
	if len(messages) != 1 {
		t.Fatalf("got %d messages, want 1", len(messages))
	}
	if !messages[0].IsSuperchat {
		t.Error("a paid message must be recognised as a super chat")
	}
	if messages[0].Amount != "Rp50.000" {
		t.Errorf("amount = %q", messages[0].Amount)
	}
}

// Somebody paying and saying nothing is still worth announcing, so an empty
// message must not drop the whole thing.
func TestAPaidMessageWithNoWordsSurvives(t *testing.T) {
	transport, _ := serve(t, page("first-token"), answer(5000, chatItemJSON(
		"liveChatPaidStickerRenderer",
		`{"id":"sticker-1","timestampUsec":"1756600000000000",`+
			`"authorName":{"simpleText":"Sari"},`+
			`"purchaseAmountText":{"simpleText":"Rp10.000"},`+
			`"stickerAccessibility":{"accessibilityData":{"label":"stiker kucing"}}}`)))

	messages := collectScraped(t, transport)
	if len(messages) != 1 || !messages[0].IsSuperchat {
		t.Fatalf("got %+v", messages)
	}
	if messages[0].Text != "stiker kucing" {
		t.Errorf("text = %q, want the sticker's own label", messages[0].Text)
	}
}

// Owner and moderator come from badge icons; a member is told apart by having
// the channel's own image instead of one of YouTube's icons.
func TestBadgesDecideOwnerModeratorAndMember(t *testing.T) {
	transport, _ := serve(t, page("first-token"), answer(5000,
		chatItemJSON("liveChatTextMessageRenderer",
			`{"id":"a","timestampUsec":"1756600000000000","authorName":{"simpleText":"Mikki"},`+
				`"message":{"runs":[{"text":"owner"}]},"authorBadges":[`+
				`{"liveChatAuthorBadgeRenderer":{"icon":{"iconType":"OWNER"}}}]}`),
		chatItemJSON("liveChatTextMessageRenderer",
			`{"id":"b","timestampUsec":"1756600000000001","authorName":{"simpleText":"Mod"},`+
				`"message":{"runs":[{"text":"mod"}]},"authorBadges":[`+
				`{"liveChatAuthorBadgeRenderer":{"icon":{"iconType":"MODERATOR"}}}]}`),
		chatItemJSON("liveChatTextMessageRenderer",
			`{"id":"c","timestampUsec":"1756600000000002","authorName":{"simpleText":"Fan"},`+
				`"message":{"runs":[{"text":"member"}]},"authorBadges":[`+
				`{"liveChatAuthorBadgeRenderer":{"tooltip":"Member (6 months)",`+
				`"customThumbnail":{"thumbnails":[]}}}]}`)))

	messages := collectScraped(t, transport)
	if len(messages) != 3 {
		t.Fatalf("got %d messages, want 3", len(messages))
	}
	if !messages[0].IsOwner {
		t.Error("the owner badge must be recognised")
	}
	if !messages[1].IsModerator {
		t.Error("the moderator badge must be recognised")
	}
	if !messages[2].IsMember {
		t.Error("a custom badge image is what marks a member")
	}
}

// A channel's own emote has no character to read, only a name like :_wave:.
// Reading the raw shortcut aloud with its punctuation would be worse than the
// name on its own.
func TestEmojiAreFlattenedIntoSomethingSpeakable(t *testing.T) {
	transport, _ := serve(t, page("first-token"), answer(5000, chatItemJSON(
		"liveChatTextMessageRenderer",
		`{"id":"e","timestampUsec":"1756600000000000","authorName":{"simpleText":"Rina"},`+
			`"message":{"runs":[{"text":"hai "},`+
			`{"emoji":{"emojiId":"👋","shortcuts":[":wave:"],"isCustomEmoji":false}},`+
			`{"text":" "},`+
			`{"emoji":{"emojiId":"UC/abc","shortcuts":[":_lambai:"],"isCustomEmoji":true}}]}}`)))

	messages := collectScraped(t, transport)
	if len(messages) != 1 {
		t.Fatalf("got %d messages, want 1", len(messages))
	}
	if !strings.Contains(messages[0].Text, "👋") {
		t.Errorf("a standard emoji keeps its character, got %q", messages[0].Text)
	}
	if !strings.Contains(messages[0].Text, "lambai") {
		t.Errorf("a custom emote falls back to its name, got %q", messages[0].Text)
	}
	if strings.Contains(messages[0].Text, ":_") {
		t.Errorf("the shortcut's punctuation must not be read aloud, got %q", messages[0].Text)
	}
}

// Deletions, pinned banners and poll results all arrive in the same stream.
// They are not things a viewer said, and reading them aloud would be noise.
func TestActionsThatAreNotMessagesAreIgnored(t *testing.T) {
	transport, _ := serve(t, page("first-token"), answer(5000,
		`{"markChatItemAsDeletedAction":{"targetItemId":"msg-1"}}`,
		chatItemJSON("liveChatViewerEngagementMessageRenderer",
			`{"id":"x","message":{"runs":[{"text":"Welcome to live chat!"}]}}`),
		chatItemJSON("liveChatTextMessageRenderer",
			`{"id":"real","timestampUsec":"1756600000000000",`+
				`"authorName":{"simpleText":"Rina"},"message":{"runs":[{"text":"halo"}]}}`)))

	messages := collectScraped(t, transport)
	if len(messages) != 1 || messages[0].ID != "real" {
		t.Errorf("got %+v, want only the one real message", messages)
	}
}

// Chat being switched off is permanent for this broadcast. It has to arrive as
// its own error, or the ingest loop retries it on a two second backoff and
// turns it into a stream of spoken announcements she cannot switch off.
func TestAPageWithNoChatIsReportedAsUnavailable(t *testing.T) {
	transport, _ := serve(t, noChatPage)

	err := transport.Run(context.Background(), Target{VideoID: "abcdefghijk"},
		func([]Message) {}, func() {})

	var missing *youtube.ChatUnavailableError
	if !errors.As(err, &missing) {
		t.Fatalf("err = %#v, want a ChatUnavailableError", err)
	}
}

// Without a video id the page cannot be fetched at all, but the Data API
// transports may still work from a live chat id -- so this must be an ordinary
// failure that falls through, not a permanent one that stops the chain.
func TestNoVideoIDFallsThroughRatherThanStopping(t *testing.T) {
	transport := &scrapeTransport{}

	err := transport.Run(context.Background(), Target{LiveChatID: "chat-1"},
		func([]Message) {}, func() {})

	if err == nil {
		t.Fatal("there was nothing to fetch, so this must fail")
	}
	var missing *youtube.ChatUnavailableError
	if errors.As(err, &missing) {
		t.Error("this must not stop the fallback chain: the API transports can still work")
	}
}

// Connected is claimed once messages are genuinely arriving, not when the page
// has merely been fetched.
func TestReadyIsOnlyClaimedOnceTheEndpointAnswers(t *testing.T) {
	transport, _ := serve(t, page("first-token"), answer(5000))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ready := make(chan struct{}, 1)
	go func() {
		_ = transport.Run(ctx, Target{VideoID: "abcdefghijk"}, func([]Message) {}, func() {
			select {
			case ready <- struct{}{}:
			default:
			}
		})
	}()

	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatal("an answer with no messages in it is still proof that chat works")
	}
}

// A missing or absurd timeout must not turn into a tight loop against
// YouTube's front end.
func TestThePollIntervalIsHeldBetweenItsFloorAndCeiling(t *testing.T) {
	for name, timeout := range map[string]int{"none": 0, "absurd": 3600000} {
		transport, _ := serve(t, page("first-token"), answer(timeout))
		session, err := transport.open(context.Background(), "abcdefghijk")
		if err != nil {
			t.Fatal(err)
		}
		_, wait, err := transport.next(context.Background(), session)
		if err != nil {
			t.Fatal(err)
		}
		if wait < minScrapeWait || wait > maxScrapeWait {
			t.Errorf("%s: wait = %v, outside [%v, %v]", name, wait, minScrapeWait, maxScrapeWait)
		}
	}
}

// -- pulling the data out of the page -----------------------------------------

// Cutting at the end of the line would be simpler and wrong: the page is one
// minified line and it ends long after the object does.
func TestTheChatDataIsCutAtItsOwnClosingBrace(t *testing.T) {
	extracted := extractJSON(page("token"), `window["ytInitialData"] =`)
	if len(extracted) == 0 {
		t.Fatal("nothing was extracted")
	}

	var into map[string]any
	if err := json.Unmarshal(extracted, &into); err != nil {
		t.Fatalf("what was extracted is not valid json: %v", err)
	}
	if strings.Contains(string(extracted), "unrelated") {
		t.Error("the object ran past its own closing brace")
	}
}

// Braces inside a chat message are an ordinary thing for somebody to type.
func TestABraceInsideAMessageDoesNotEndTheObjectEarly(t *testing.T) {
	body := `window["ytInitialData"] = {"text":"a } and a \" quote","after":1};tail`

	extracted := extractJSON(body, `window["ytInitialData"] =`)
	var into struct {
		Text  string `json:"text"`
		After int    `json:"after"`
	}
	if err := json.Unmarshal(extracted, &into); err != nil {
		t.Fatalf("extracted %q: %v", extracted, err)
	}
	if into.After != 1 {
		t.Errorf("the object was cut short at a brace inside a string: %q", extracted)
	}
}

// The read cursor compares these as text. A trimmed fraction sorts wrongly
// against an untrimmed one, which shows up as a handful of messages being read
// aloud a second time after a restart.
func TestTimestampsSortAsTextInTheOrderTheyHappened(t *testing.T) {
	earlier := publishedAt("1756600000000000")
	later := publishedAt("1756600000500000")
	muchLater := publishedAt("1756600001000000")

	if !(earlier < later && later < muchLater) {
		t.Errorf("out of order as text: %q, %q, %q", earlier, later, muchLater)
	}
	if len(earlier) != len(muchLater) {
		t.Errorf("widths differ, so text comparison is unsafe: %q vs %q", earlier, muchLater)
	}
}
