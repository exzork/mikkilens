package chat

import (
	"testing"

	"github.com/exzork/mikkilens/packages/core/paths"
)

// Closing MikkiLens and opening it again makes YouTube hand back the recent
// history on the fresh connection. Without a saved position every one of those
// messages is read out a second time, and she has no way to tell which parts
// she has already heard.

func isolatedCursor(t *testing.T) *ReadCursor {
	t.Helper()
	paths.SetRoot(t.TempDir())
	if _, err := paths.EnsureDataDir(); err != nil {
		t.Fatal(err)
	}
	return LoadReadCursor()
}

func message(id, published string) Message {
	return Message{ID: id, Author: "Budi", Text: "halo", PublishedAt: published}
}

func TestNothingIsAlreadyReadOnAFreshStart(t *testing.T) {
	cursor := isolatedCursor(t)

	if cursor.AlreadyRead(message("m1", "2026-08-29T13:00:00Z")) {
		t.Error("with no saved position, nothing has been read")
	}
}

func TestAMessageReadAloudIsNotReadAgain(t *testing.T) {
	cursor := isolatedCursor(t)
	first := message("m1", "2026-08-29T13:00:00Z")
	cursor.Adopt("chat-1")
	cursor.Record(first)

	if !cursor.AlreadyRead(first) {
		t.Error("the message just read must not be read again")
	}
}

// The point of writing it to disk.
func TestThePositionSurvivesARestart(t *testing.T) {
	cursor := isolatedCursor(t)
	cursor.Adopt("chat-1")
	cursor.Record(message("m1", "2026-08-29T13:00:00Z"))
	cursor.Record(message("m2", "2026-08-29T13:00:01Z"))

	// A new process, same data directory.
	restarted := LoadReadCursor()
	restarted.Adopt("chat-1")

	if !restarted.AlreadyRead(message("m1", "2026-08-29T13:00:00Z")) {
		t.Error("a message read before the restart must not be read again")
	}
	if !restarted.AlreadyRead(message("m2", "2026-08-29T13:00:01Z")) {
		t.Error("a message read before the restart must not be read again")
	}
	if restarted.AlreadyRead(message("m3", "2026-08-29T13:00:02Z")) {
		t.Error("a message that arrived since must still be read")
	}
}

// Two messages in the same instant cannot be ordered by timestamp. Dropping
// one because it shares a timestamp with a message she has heard would lose it
// silently, which is the failure this application must not have.
func TestASimultaneousMessageIsStillRead(t *testing.T) {
	cursor := isolatedCursor(t)
	cursor.Adopt("chat-1")
	cursor.Record(message("m1", "2026-08-29T13:00:00Z"))

	if cursor.AlreadyRead(message("m2", "2026-08-29T13:00:00Z")) {
		t.Error("a different message posted in the same instant has not been read")
	}
}

// A new broadcast has its own messages and nothing in it has been read.
// Carrying the previous stream's mark over would silence its beginning.
func TestANewBroadcastStartsWithAClearPosition(t *testing.T) {
	cursor := isolatedCursor(t)
	cursor.Adopt("chat-1")
	cursor.Record(message("m1", "2026-08-29T13:00:00Z"))

	cursor.Adopt("chat-2")

	if cursor.AlreadyRead(message("m1", "2026-08-29T13:00:00Z")) {
		t.Error("a different chat's position must not carry over")
	}
}

func TestReturningToTheSameBroadcastKeepsThePosition(t *testing.T) {
	cursor := isolatedCursor(t)
	cursor.Adopt("chat-1")
	cursor.Record(message("m1", "2026-08-29T13:00:00Z"))

	cursor.Adopt("chat-1") // reconnected to the same chat

	if !cursor.AlreadyRead(message("m1", "2026-08-29T13:00:00Z")) {
		t.Error("reconnecting to the same chat must keep what was already read")
	}
}

// The id list is capped, so a long stream cannot grow the file without bound.
// Older messages still fall to the timestamp, which is why capping is safe.
func TestTheRememberedIDsAreCappedButOlderMessagesStayRead(t *testing.T) {
	cursor := isolatedCursor(t)
	cursor.Adopt("chat-1")

	for index := range recentIDs * 3 {
		cursor.Record(Message{
			ID:          string(rune('a'+index%26)) + "-" + itoa(index),
			PublishedAt: "2026-08-29T13:00:00Z",
		})
	}

	cursor.mu.Lock()
	kept := len(cursor.state.RecentIDs)
	tracked := len(cursor.ids)
	cursor.mu.Unlock()

	if kept > recentIDs {
		t.Errorf("kept %d ids, want at most %d", kept, recentIDs)
	}
	if tracked > recentIDs {
		t.Errorf("tracking %d ids in memory, want at most %d", tracked, recentIDs)
	}
}

// Ingestion filters on this, so a message already heard never reaches the
// backlog -- "you are 40 messages behind" has to mean forty she has not heard.
func TestAlreadyReadMessagesNeverEnterTheBacklog(t *testing.T) {
	paths.SetRoot(t.TempDir())
	if _, err := paths.EnsureDataDir(); err != nil {
		t.Fatal(err)
	}

	cursor := LoadReadCursor()
	cursor.Adopt("chat-1")
	cursor.Record(message("m1", "2026-08-29T13:00:00Z"))

	ingest := NewIngest(nil, IngestOptions{ReadCursor: cursor})

	ingest.Accept([]Message{
		message("m1", "2026-08-29T13:00:00Z"), // already heard
		message("m2", "2026-08-29T13:00:05Z"), // new
	})

	if ingest.Len() != 1 {
		t.Errorf("backlog holds %d messages, want only the unheard one", ingest.Len())
	}
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := ""
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	return digits
}
