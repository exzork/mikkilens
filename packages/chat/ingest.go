// Package chat ingests live chat and reads it aloud.
//
// Ingestion and playback are decoupled on purpose. The connection never stops;
// pausing only moves a cursor. That is what makes "pause, then catch up" a
// real promise rather than a hopeful one: a pause can never lose a message.
//
// Transport matters for quota. Google recommends the streaming endpoint, which
// holds a connection open and pushes messages as they arrive, precisely
// because polling "reduces the need for constant polling and helps to avoid
// exceeding your quota". Polling every five seconds across a four hour stream
// would exhaust the default daily allowance on its own. The streaming
// endpoint's wire format is not fully documented, so it is tried first and the
// poller takes over automatically if it does not work.
package chat

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	yt "google.golang.org/api/youtube/v3"

	"github.com/exzork/mikkilens/packages/controllers/youtube"
)

// maxBuffer caps the backlog. A stream busy enough to overflow it has bigger
// problems than a lost message from two thousand messages ago.
const maxBuffer = 2000

// Message is one thing a viewer said.
type Message struct {
	ID          string  `json:"id"`
	Author      string  `json:"author"`
	Text        string  `json:"text"`
	PublishedAt string  `json:"published_at"`
	ReceivedAt  float64 `json:"received_at"`
	IsSuperchat bool    `json:"is_superchat"`
	IsMember    bool    `json:"is_member"`
	Amount      string  `json:"amount"`
	IsOwner     bool    `json:"is_owner"`
	IsModerator bool    `json:"is_moderator"`
}

// IsEmoteOnly reports whether the message carries no readable words. Reading
// out "🎉🎉🎉" as nothing at all is better than reading it as silence.
func (m Message) IsEmoteOnly() bool {
	for _, character := range m.Text {
		if isAlphanumeric(character) {
			return false
		}
	}
	return true
}

func isAlphanumeric(character rune) bool {
	switch {
	case character >= '0' && character <= '9',
		character >= 'a' && character <= 'z',
		character >= 'A' && character <= 'Z',
		character > 0x7F && isLetterLike(character):
		return true
	}
	return false
}

// isLetterLike covers non-Latin scripts without pulling in the whole unicode
// table for a check that runs on every message.
func isLetterLike(character rune) bool {
	return (character >= 0x00C0 && character <= 0x024F) || // Latin extended
		(character >= 0x0370 && character <= 0x1FFF) || // Greek through to SE Asian
		(character >= 0x2C00 && character <= 0xD7FF) || // CJK, Hangul and friends
		(character >= 0xF900 && character <= 0xFDCF)
}

// ParseMessage turns one API item into a message, or reports that there was
// nothing readable in it.
func ParseMessage(item *yt.LiveChatMessage) (Message, bool) {
	if item == nil || item.Snippet == nil {
		return Message{}, false
	}
	snippet := item.Snippet

	text := snippet.DisplayMessage
	amount := ""
	isSuperchat := snippet.Type == "superChatEvent" || snippet.Type == "superStickerEvent"
	isMember := snippet.Type == "newSponsorEvent" || snippet.Type == "memberMilestoneChatEvent"

	if isSuperchat {
		switch {
		case snippet.SuperChatDetails != nil:
			amount = snippet.SuperChatDetails.AmountDisplayString
			if text == "" {
				text = snippet.SuperChatDetails.UserComment
			}
		case snippet.SuperStickerDetails != nil:
			amount = snippet.SuperStickerDetails.AmountDisplayString
		}
	}
	if text == "" && !isSuperchat && !isMember {
		return Message{}, false
	}

	author := "seseorang"
	isOwner, isModerator := false, false
	if item.AuthorDetails != nil {
		if item.AuthorDetails.DisplayName != "" {
			author = item.AuthorDetails.DisplayName
		}
		isOwner = item.AuthorDetails.IsChatOwner
		isModerator = item.AuthorDetails.IsChatModerator
	}

	return Message{
		ID:          item.Id,
		Author:      author,
		Text:        text,
		PublishedAt: snippet.PublishedAt,
		ReceivedAt:  float64(time.Now().UnixNano()) / 1e9,
		IsSuperchat: isSuperchat,
		IsMember:    isMember,
		Amount:      amount,
		IsOwner:     isOwner,
		IsModerator: isModerator,
	}, true
}

// Transport is one way of getting messages out of YouTube.
type Transport interface {
	Name() string
	Run(ctx context.Context, liveChatID string, deliver func([]Message)) error
}

// IngestOptions configure ingestion.
type IngestOptions struct {
	Transport string // "auto" | "stream" | "poll"

	OnMessage      func(Message)
	OnStatus       func(state, detail string)
	OnQuotaWarning func(percent int)
}

// Ingest holds the connection and the backlog. It never stops on its own.
type Ingest struct {
	mu       sync.RWMutex
	youtube  *youtube.Controller
	options  IngestOptions
	messages []Message
	seen     map[string]bool

	transportInUse string
	lastError      string

	running bool
	cancel  context.CancelFunc
	done    chan struct{}
}

// NewIngest prepares ingestion. Nothing connects until Start.
func NewIngest(controller *youtube.Controller, options IngestOptions) *Ingest {
	return &Ingest{
		youtube: controller,
		options: options,
		seen:    map[string]bool{},
	}
}

// Start begins collecting messages.
func (i *Ingest) Start() {
	i.mu.Lock()
	if i.running {
		i.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	i.running, i.cancel, i.done = true, cancel, make(chan struct{})
	done := i.done
	i.mu.Unlock()

	go i.run(ctx, done)
}

// Stop ends collection.
func (i *Ingest) Stop() {
	i.mu.Lock()
	if !i.running {
		i.mu.Unlock()
		return
	}
	i.running = false
	cancel, done := i.cancel, i.done
	i.mu.Unlock()

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
	}
}

// Running reports whether ingestion is live.
func (i *Ingest) Running() bool {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.running
}

// TransportInUse names the transport that is actually connected.
func (i *Ingest) TransportInUse() string {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.transportInUse
}

// LastError is the most recent failure, for the settings page.
func (i *Ingest) LastError() string {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.lastError
}

// Transports lists the transports to try, in order.
func (i *Ingest) Transports() []Transport {
	poller := &pollingTransport{youtube: i.youtube, onQuotaWarning: i.options.OnQuotaWarning}
	switch i.options.Transport {
	case "poll":
		return []Transport{poller}
	case "stream":
		return []Transport{&streamTransport{youtube: i.youtube}}
	default:
		// Streaming first, for quota reasons; polling as the fallback.
		return []Transport{&streamTransport{youtube: i.youtube}, poller}
	}
}

func (i *Ingest) run(ctx context.Context, done chan struct{}) {
	defer close(done)

	candidates := i.Transports()
	delay := 2 * time.Second

	for ctx.Err() == nil {
		liveChatID, err := i.youtube.LiveChatID(ctx)
		if err != nil {
			i.note(err.Error())
			i.status("waiting", err.Error())
			if sleep(ctx, 15*time.Second) {
				return
			}
			continue
		}

		for index, transport := range candidates {
			if ctx.Err() != nil {
				return
			}

			i.mu.Lock()
			i.transportInUse = transport.Name()
			i.mu.Unlock()
			i.status("connected", transport.Name())

			err := transport.Run(ctx, liveChatID, i.accept)
			if ctx.Err() != nil {
				return
			}
			if err == nil {
				delay = 2 * time.Second
				break
			}

			i.note(err.Error())
			if _, exhausted := err.(*youtube.QuotaExhaustedError); exhausted {
				i.status("quota", err.Error())
				if sleep(ctx, 5*time.Minute) {
					return
				}
				break
			}

			slog.Warn("chat transport failed", "transport", transport.Name(), "error", err)
			if index+1 < len(candidates) {
				slog.Info("falling back", "transport", candidates[index+1].Name())
				continue
			}
			i.status("disconnected", err.Error())
			if sleep(ctx, delay) {
				return
			}
			delay = min(60*time.Second, delay*2)
		}
	}
}

// Accept adds messages, dropping the ones already seen. It is exported so
// tests can deliver messages without a network.
func (i *Ingest) Accept(messages []Message) { i.accept(messages) }

func (i *Ingest) accept(messages []Message) {
	fresh := make([]Message, 0, len(messages))

	i.mu.Lock()
	for _, message := range messages {
		if message.ID != "" {
			if i.seen[message.ID] {
				continue
			}
			i.seen[message.ID] = true
		}
		i.messages = append(i.messages, message)
		fresh = append(fresh, message)
	}
	if len(i.messages) > maxBuffer {
		i.messages = i.messages[len(i.messages)-maxBuffer:]
	}
	// Keep the seen set from growing without bound over a long stream.
	if len(i.seen) > maxBuffer*2 {
		rebuilt := make(map[string]bool, len(i.messages))
		for _, message := range i.messages {
			if message.ID != "" {
				rebuilt[message.ID] = true
			}
		}
		i.seen = rebuilt
	}
	callback := i.options.OnMessage
	i.mu.Unlock()

	if callback == nil {
		return
	}
	for _, message := range fresh {
		callback(message)
	}
}

// SetOnMessage changes the new-message callback.
func (i *Ingest) SetOnMessage(callback func(Message)) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.options.OnMessage = callback
}

// Snapshot is everything collected so far.
func (i *Ingest) Snapshot() []Message {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return append([]Message(nil), i.messages...)
}

// Len is how many messages are held.
func (i *Ingest) Len() int {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return len(i.messages)
}

// From returns the messages at or after one index, plus the current total.
func (i *Ingest) From(index int) ([]Message, int) {
	i.mu.RLock()
	defer i.mu.RUnlock()
	if index < 0 {
		index = 0
	}
	if index >= len(i.messages) {
		return nil, len(i.messages)
	}
	return append([]Message(nil), i.messages[index:]...), len(i.messages)
}

// Remove drops one message by index, which is how a super chat jumps the queue
// without disturbing the order of everything behind it.
func (i *Ingest) Remove(index int) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if index < 0 || index >= len(i.messages) {
		return
	}
	i.messages = append(i.messages[:index], i.messages[index+1:]...)
}

func (i *Ingest) note(reason string) {
	i.mu.Lock()
	i.lastError = reason
	i.mu.Unlock()
}

func (i *Ingest) status(state, detail string) {
	i.mu.RLock()
	callback := i.options.OnStatus
	i.mu.RUnlock()
	if callback == nil {
		return
	}
	defer func() {
		if problem := recover(); problem != nil {
			slog.Error("chat status callback panicked", "panic", problem)
		}
	}()
	callback(state, detail)
}

// sleep waits, and reports whether the context ended first.
func sleep(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return true
	case <-timer.C:
		return false
	}
}

func trimTo(text string, limit int) string {
	runes := []rune(strings.TrimSpace(text))
	if limit <= 0 || len(runes) <= limit {
		return string(runes)
	}
	return strings.TrimRight(string(runes[:limit]), " ") + "…"
}
