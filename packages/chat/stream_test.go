package chat

import (
	"context"
	"strings"
	"testing"
	"time"
)

// The streaming endpoint answers with a JSON array written a piece at a time.
// Decoding it as one value waits for an array that has not finished, so chat
// connected successfully and then read nothing at all -- the worst shape of
// failure here, because everything looks fine.

const oneBatch = `{"items":[{"id":"m1","snippet":{"type":"textMessageEvent",` +
	`"displayMessage":"halo","publishedAt":"2026-08-29T13:00:00Z"},` +
	`"authorDetails":{"displayName":"Budi"}}],"nextPageToken":"page-2"}`

func collect(t *testing.T, body string) ([]Message, string) {
	t.Helper()
	transport := &streamTransport{}

	var got []Message
	page, err := transport.consume(context.Background(), strings.NewReader(body),
		func(batch []Message) { got = append(got, batch...) })
	if err != nil {
		t.Fatalf("consume: %v", err)
	}
	return got, page
}

func TestAnArrayFramedStreamIsRead(t *testing.T) {
	messages, page := collect(t, "["+oneBatch+"]")

	if len(messages) != 1 {
		t.Fatalf("read %d messages, want 1", len(messages))
	}
	if messages[0].Author != "Budi" || messages[0].Text != "halo" {
		t.Errorf("read %+v", messages[0])
	}
	if page != "page-2" {
		t.Errorf("next page is %q, want page-2", page)
	}
}

// Several batches arrive in the one array over the life of the connection.
func TestEveryBatchInTheArrayIsRead(t *testing.T) {
	messages, _ := collect(t, "["+oneBatch+","+oneBatch+","+oneBatch+"]")

	if len(messages) != 3 {
		t.Fatalf("read %d messages, want 3", len(messages))
	}
}

// Leading whitespace before the "[" must not be mistaken for the object
// framing and send the decoder down the wrong path.
func TestWhitespaceBeforeTheArrayIsIgnored(t *testing.T) {
	messages, _ := collect(t, "\n  \t["+oneBatch+"]")

	if len(messages) != 1 {
		t.Fatalf("read %d messages, want 1", len(messages))
	}
}

// If the framing ever changes back, this degrades to the older behaviour
// rather than to silence.
func TestABareSequenceOfObjectsStillWorks(t *testing.T) {
	messages, _ := collect(t, oneBatch+oneBatch)

	if len(messages) != 2 {
		t.Fatalf("read %d messages, want 2", len(messages))
	}
}

// An array the server has not closed is the normal case mid-stream: the
// connection ends, and what was read before that must still have been
// delivered rather than discarded with an error.
func TestAnUnclosedArrayStillDeliversWhatArrived(t *testing.T) {
	transport := &streamTransport{}

	var got []Message
	_, err := transport.consume(context.Background(),
		strings.NewReader("["+oneBatch+","+oneBatch),
		func(batch []Message) { got = append(got, batch...) })

	if err != nil {
		t.Fatalf("a truncated stream is a reconnect, not a failure: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("read %d messages, want the 2 that did arrive", len(got))
	}
}

func TestAnEmptyArrayIsNotAnError(t *testing.T) {
	messages, _ := collect(t, "[]")

	if len(messages) != 0 {
		t.Errorf("read %d messages from an empty stream", len(messages))
	}
}

// Cancelling must end the read rather than block on a stream that is idle by
// design between messages.
func TestShuttingDownEndsTheRead(t *testing.T) {
	transport := &streamTransport{}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		_, _ = transport.consume(ctx, strings.NewReader("["+oneBatch+","+oneBatch+"]"),
			func([]Message) {})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("consume did not return")
	}
}
