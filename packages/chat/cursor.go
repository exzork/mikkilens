package chat

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/exzork/mikkilens/packages/core/paths"
)

// What has already been read aloud survives a restart.
//
// Without this, closing MikkiLens and opening it again means YouTube hands
// back the recent history on the fresh connection and every one of those
// messages is read out a second time. On a quiet stream that is a minor
// annoyance; on a busy one it is minutes of hearing a conversation she has
// already had, with no way to tell which parts are new.
//
// A high-water mark by timestamp does most of the work, and a short list of
// recently read ids settles the ties -- two messages posted in the same
// instant cannot be ordered by their timestamps, and dropping one of them
// because it shares a timestamp with a message she has heard would lose it
// silently, which is the one thing this application must not do.

// recentIDs is how many message ids are remembered alongside the timestamp.
// Enough to cover any plausible burst sharing one instant, small enough that
// the file stays trivial to write.
const recentIDs = 200

// ReadCursor remembers the last message read aloud.
type ReadCursor struct {
	mu    sync.Mutex
	path  string
	state cursorState
	ids   map[string]bool
}

type cursorState struct {
	ChatID          string   `json:"chat_id"`
	LastPublishedAt string   `json:"last_published_at"`
	RecentIDs       []string `json:"recent_ids"`
}

// LoadReadCursor restores the mark, or starts a fresh one.
//
// A cursor that cannot be read is not an error worth stopping for: the cost is
// hearing some messages twice, and refusing to read chat at all would be far
// worse than that.
func LoadReadCursor() *ReadCursor {
	cursor := &ReadCursor{
		path: filepath.Join(paths.DataDir(), "chat_cursor.json"),
		ids:  map[string]bool{},
	}

	data, err := os.ReadFile(cursor.path)
	if err != nil {
		return cursor
	}
	if err := json.Unmarshal(data, &cursor.state); err != nil {
		slog.Warn("the saved chat position was unreadable; starting fresh",
			"error", err)
		cursor.state = cursorState{}
		return cursor
	}
	for _, id := range cursor.state.RecentIDs {
		cursor.ids[id] = true
	}
	return cursor
}

// Adopt points the cursor at a chat, forgetting a different one.
//
// A new broadcast has its own messages, and nothing in it has been read.
// Carrying the previous stream's mark over would silence the beginning of
// this one.
func (c *ReadCursor) Adopt(chatID string) {
	if c == nil || chatID == "" {
		return
	}
	c.mu.Lock()
	same := c.state.ChatID == chatID
	if !same {
		c.state = cursorState{ChatID: chatID}
		c.ids = map[string]bool{}
	}
	c.mu.Unlock()

	if !same {
		c.save()
	}
}

// AlreadyRead reports whether this message was read aloud before.
func (c *ReadCursor) AlreadyRead(message Message) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	if message.ID != "" && c.ids[message.ID] {
		return true
	}
	if c.state.LastPublishedAt == "" || message.PublishedAt == "" {
		return false
	}
	// RFC 3339 from one source sorts correctly as text, and comparing strings
	// avoids a parse failure quietly turning into "read everything again".
	// Strictly older only: an equal timestamp is settled by the id list above,
	// so a message posted in the same instant is still read.
	return message.PublishedAt < c.state.LastPublishedAt
}

// Record notes that a message has been read aloud.
func (c *ReadCursor) Record(message Message) {
	if c == nil {
		return
	}
	c.mu.Lock()
	if message.PublishedAt > c.state.LastPublishedAt {
		c.state.LastPublishedAt = message.PublishedAt
	}
	if message.ID != "" && !c.ids[message.ID] {
		c.ids[message.ID] = true
		c.state.RecentIDs = append(c.state.RecentIDs, message.ID)
		if len(c.state.RecentIDs) > recentIDs {
			for _, dropped := range c.state.RecentIDs[:len(c.state.RecentIDs)-recentIDs] {
				delete(c.ids, dropped)
			}
			c.state.RecentIDs = c.state.RecentIDs[len(c.state.RecentIDs)-recentIDs:]
		}
	}
	c.mu.Unlock()

	c.save()
}

// save writes the mark. It is called once per message read aloud, which is
// once every few seconds at speaking speed -- not a rate worth batching for.
func (c *ReadCursor) save() {
	if c == nil || c.path == "" {
		return
	}
	c.mu.Lock()
	encoded, err := json.Marshal(c.state)
	c.mu.Unlock()
	if err != nil {
		return
	}

	if _, err := paths.EnsureDataDir(); err != nil {
		return
	}
	// Written via a temporary file: a half-written cursor would be discarded
	// on the next start, and losing the mark means re-reading the backlog.
	temporary := c.path + ".tmp"
	if err := os.WriteFile(temporary, encoded, 0o644); err != nil {
		slog.Warn("could not save the chat position", "error", err)
		return
	}
	if err := os.Rename(temporary, c.path); err != nil {
		slog.Warn("could not save the chat position", "error", err)
		_ = os.Remove(temporary)
	}
}
