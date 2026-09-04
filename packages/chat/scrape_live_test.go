package chat

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestTheRealLiveChatPageStillHasTheShapeWeExpect is the canary.
//
// Everything else in scrape_test.go tests this transport against a page this
// repository wrote, which proves the parsing and proves nothing about YouTube.
// The page is not a published contract: it can be reshaped without notice, and
// the first sign would otherwise be chat quietly falling back to the Data API
// and eating the daily quota.
//
// It talks to the network, so it does not run by default. To run it:
//
//	MIKKILENS_LIVE_CHAT_TEST=<video id of any live stream> go test ./packages/chat -run RealLiveChat -v
func TestTheRealLiveChatPageStillHasTheShapeWeExpect(t *testing.T) {
	videoID := os.Getenv("MIKKILENS_LIVE_CHAT_TEST")
	if videoID == "" {
		t.Skip("set MIKKILENS_LIVE_CHAT_TEST to the video id of a live stream")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	transport := &scrapeTransport{}

	session, err := transport.open(ctx, videoID)
	if err != nil {
		t.Fatalf("the live chat page could not be read: %v", err)
	}
	if session.continuation == "" {
		t.Fatal("no continuation token: the page has changed shape")
	}
	if session.apiKey == "" {
		t.Error("no INNERTUBE_API_KEY on the page; the endpoint may refuse the request")
	}
	t.Logf("client version %q, api key present %v", session.clientVersion, session.apiKey != "")

	// Several rounds, because the first batch off a fresh continuation is
	// routinely empty: the token from the page points at "from now on", not at
	// the backlog. One empty answer proves nothing either way.
	total := 0
	for round := range 3 {
		messages, wait, err := transport.next(ctx, session)
		if err != nil {
			t.Fatalf("round %d: the live chat endpoint refused the continuation: %v", round, err)
		}
		if session.continuation == "" {
			t.Fatal("no continuation came back, so the next request has nothing to follow")
		}
		if wait < minScrapeWait || wait > maxScrapeWait {
			t.Errorf("wait = %v, outside [%v, %v]", wait, minScrapeWait, maxScrapeWait)
		}
		t.Logf("round %d: %d messages, next in %v", round, len(messages), wait)

		for _, message := range messages {
			if message.ID == "" {
				t.Error("a message with no id defeats the duplicate filter")
			}
			if message.PublishedAt == "" {
				t.Error("a message with no timestamp defeats the read cursor")
			}
			if message.Author == "" && message.Text == "" {
				t.Errorf("nothing readable in %+v", message)
			}
		}
		total += len(messages)

		if sleep(ctx, wait) {
			break
		}
	}

	// A genuinely quiet chat returns nothing and that is not a failure, so
	// this is reported rather than asserted. On a busy stream, zero here is
	// the signal that the shape has changed under us.
	if total == 0 {
		t.Log("no messages arrived: either the chat is quiet, or the shape has changed")
	}
}
