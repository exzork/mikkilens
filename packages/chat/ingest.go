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
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	yt "google.golang.org/api/youtube/v3"

	"github.com/exzork/mikkilens/packages/controllers/youtube"
)

// chatRecheckInterval is how long to wait before looking again at a broadcast
// that has no chat. Long enough that nothing is hammered, short enough that
// turning chat on is noticed within a few minutes.
const chatRecheckInterval = 2 * time.Minute

// rateLimitBackoff is how long to wait when Google says we are asking too
// fast. Long enough to actually clear the burst that caused it.
const rateLimitBackoff = 30 * time.Second

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

	// AuthorChannelID is who said it, as YouTube identifies them. It is kept
	// because a gifted membership names its giver by id and nothing else.
	AuthorChannelID string `json:"author_channel_id"`

	// A gifted membership arrives as two kinds of event: one from the giver
	// saying how many they bought, and then one per recipient. The count is
	// the whole point of the first -- one is a kindness and fifty is an event,
	// and reading them the same way loses the difference.
	IsGift         bool   `json:"is_gift"`
	IsGiftReceived bool   `json:"is_gift_received"`
	GiftCount      int    `json:"gift_count"`
	GiftLevel      string `json:"gift_level"`

	// GifterChannelID is who paid for a received membership, and GifterName is
	// that id resolved against the giving message. The name is empty when the
	// giving message was never seen -- joining a stream halfway through, most
	// often -- and the sentence is written to still make sense without it.
	GifterChannelID string `json:"gifter_channel_id"`
	GifterName      string `json:"gifter_name"`

	// GiftIndex is this recipient's place in the batch and GiftTotal is how
	// many were bought. Together they are what lets a run of fifty be read as
	// a few names and a count.
	GiftIndex int `json:"gift_index"`
	GiftTotal int `json:"gift_total"`

	// MemberLevel is the tier the channel sells, named by whoever set it up,
	// and MemberMonths is how long they have kept it. Both are worth saying:
	// the level is what somebody chose to pay for, and "member for two years"
	// is a different thing to acknowledge than someone who joined today.
	MemberLevel   string `json:"member_level"`
	MemberMonths  int    `json:"member_months"`
	IsMemberUpped bool   `json:"is_member_upgraded"`
}

// IsEvent reports whether this is an announcement rather than somebody typing.
//
// These carry no words of their own -- the event is the message -- so the
// filters that drop wordless chat have to leave them alone or every membership
// on the stream goes unheard.
func (m Message) IsEvent() bool {
	return m.IsSuperchat || m.IsMember || m.IsGift || m.IsGiftReceived
}

// IsPaid reports whether somebody spent money on this.
//
// It is what puts a message in the same tier as a Tako or Trakteer alert:
// a purchase arrives once and matters then, where chat is a stream that will
// still be there in a minute.
func (m Message) IsPaid() bool { return m.IsSuperchat || m.IsGift }

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
	isGift := snippet.Type == "membershipGiftingEvent"
	isGiftReceived := snippet.Type == "giftMembershipReceivedEvent"

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

	memberLevel, memberMonths, isUpgrade := "", 0, false
	if snippet.NewSponsorDetails != nil {
		memberLevel = snippet.NewSponsorDetails.MemberLevelName
		isUpgrade = snippet.NewSponsorDetails.IsUpgrade
	}
	if snippet.MemberMilestoneChatDetails != nil {
		memberLevel = snippet.MemberMilestoneChatDetails.MemberLevelName
		memberMonths = int(snippet.MemberMilestoneChatDetails.MemberMonth)
		if text == "" {
			// A milestone can carry a comment, and it arrives here rather than
			// in the ordinary message field.
			text = snippet.MemberMilestoneChatDetails.UserComment
		}
	}

	giftCount, giftLevel, gifterID := 0, "", ""
	if isGift && snippet.MembershipGiftingDetails != nil {
		giftCount = int(snippet.MembershipGiftingDetails.GiftMembershipsCount)
		giftLevel = snippet.MembershipGiftingDetails.GiftMembershipsLevelName
	}
	if isGiftReceived && snippet.GiftMembershipReceivedDetails != nil {
		// Only the gifter's channel id, never their name. The name comes from
		// the gifting message that goes with it, which is why the ingest keeps
		// a note of who gave what -- see resolveGifters.
		gifterID = snippet.GiftMembershipReceivedDetails.GifterChannelId
		giftLevel = snippet.GiftMembershipReceivedDetails.MemberLevelName
	}

	// These four carry no words of their own: the event is the message.
	if text == "" && !isSuperchat && !isMember && !isGift && !isGiftReceived {
		return Message{}, false
	}

	author, authorID := "seseorang", ""
	isOwner, isModerator := false, false
	if item.AuthorDetails != nil {
		if item.AuthorDetails.DisplayName != "" {
			author = item.AuthorDetails.DisplayName
		}
		authorID = item.AuthorDetails.ChannelId
		isOwner = item.AuthorDetails.IsChatOwner
		isModerator = item.AuthorDetails.IsChatModerator
	}

	return Message{
		ID:              item.Id,
		Author:          author,
		AuthorChannelID: authorID,
		Text:            text,
		PublishedAt:     snippet.PublishedAt,
		ReceivedAt:      float64(time.Now().UnixNano()) / 1e9,
		IsSuperchat:     isSuperchat,
		IsMember:        isMember,
		Amount:          amount,
		IsOwner:         isOwner,
		IsModerator:     isModerator,
		IsGift:          isGift,
		IsGiftReceived:  isGiftReceived,
		GiftCount:       giftCount,
		GiftLevel:       giftLevel,
		GifterChannelID: gifterID,
		MemberLevel:     memberLevel,
		MemberMonths:    memberMonths,
		IsMemberUpped:   isUpgrade,
	}, true
}

// Target names the stream whose chat is to be read.
//
// Two identifiers rather than one, because the transports do not agree on
// which they need. The Data API works from a live chat id; the public page
// works from the video id and never learns the chat id at all. Carrying both
// lets the same loop feed either, and lets a fallback happen without going
// back to YouTube to ask a second question.
type Target struct {
	VideoID    string
	LiveChatID string
}

// cursorKey identifies this chat for the read cursor.
//
// The chat id is preferred because it is what the cursor has always been keyed
// on, so an upgrade does not lose her place. The video id stands in when there
// is no chat id, which is the ordinary case with no API key at all.
func (t Target) cursorKey() string {
	if t.LiveChatID != "" {
		return t.LiveChatID
	}
	return t.VideoID
}

// Transport is one way of getting messages out of YouTube.
type Transport interface {
	Name() string
	// Run delivers messages until it fails or the context ends. It calls
	// ready the first time it is genuinely receiving -- not when it starts
	// trying -- so that "chat is connected" is never announced about a
	// connection that is one moment away from failing.
	Run(ctx context.Context, target Target, deliver func([]Message), ready func()) error
}

// IngestOptions configure ingestion.
type IngestOptions struct {
	Transport string // "auto" | "page" | "api" | "stream" | "poll"

	OnMessage      func(Message)
	OnStatus       func(state, detail string)
	OnQuotaWarning func(percent int)

	// ReadCursor remembers what has already been read aloud, across restarts.
	// Nil means no memory, which is what a test wants: persistence is a
	// property of the running application, not of ingestion itself.
	ReadCursor *ReadCursor
}

// Ingest holds the connection and the backlog. It never stops on its own.
type Ingest struct {
	mu       sync.RWMutex
	youtube  *youtube.Controller
	options  IngestOptions
	messages []Message
	seen     map[string]bool

	// gifters is who gave a membership, by channel id, so a recipient message
	// can name them.
	gifters map[string]*gifterNote

	transportInUse string
	lastError      string

	// What was already read aloud, remembered across restarts. Filtering here
	// rather than in the reader means an already-heard message never counts
	// towards the backlog either -- "you are 40 messages behind" has to mean
	// forty she has not heard.
	//
	// Set once at construction and never reassigned, so it is read without the
	// mutex; the cursor does its own locking.
	read *ReadCursor

	running bool
	cancel  context.CancelFunc
	done    chan struct{}

	// recheck wakes the loop out of a wait. Clearing a cache is not enough on
	// its own: after being told this broadcast has no chat, the loop is asleep
	// for minutes, and going live again should be noticed at once rather than
	// whenever it happens to look next.
	recheck chan struct{}
}

// NewIngest prepares ingestion. Nothing connects until Start.
func NewIngest(controller *youtube.Controller, options IngestOptions) *Ingest {
	return &Ingest{
		youtube: controller,
		options: options,
		seen:    map[string]bool{},
		read:    options.ReadCursor,
		// Buffered by one: a nudge that arrives while the loop is working is
		// remembered rather than dropped, and a nudge never blocks its caller.
		recheck: make(chan struct{}, 1),
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

// Recheck asks the loop to look again now, instead of finishing its wait.
//
// Called when something has happened that plausibly changes the answer --
// going live, ending a stream -- so that "she started a stream with chat
// switched on" is noticed in seconds rather than minutes.
func (i *Ingest) Recheck() {
	i.mu.RLock()
	wake := i.recheck
	i.mu.RUnlock()
	if wake == nil {
		return
	}
	select {
	case wake <- struct{}{}:
	default: // already pending, and one is enough
	}
}

// waitOrRecheck sleeps, unless it is woken or the context ends. It reports
// whether the caller should stop.
func (i *Ingest) waitOrRecheck(ctx context.Context, delay time.Duration) bool {
	i.mu.RLock()
	wake := i.recheck
	i.mu.RUnlock()

	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return true
	case <-wake:
		return false
	case <-timer.C:
		return false
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
//
// The page comes first because it costs nothing: no key, no sign-in, no quota.
// The two Data API transports sit behind it as the answer to the page changing
// shape, which it may do without warning -- streaming ahead of polling, for
// the same quota reasons as before.
func (i *Ingest) Transports() []Transport {
	page := &scrapeTransport{}
	streamer := &streamTransport{youtube: i.youtube}
	poller := &pollingTransport{youtube: i.youtube, onQuotaWarning: i.options.OnQuotaWarning}
	switch i.options.Transport {
	case "page":
		return []Transport{page}
	case "poll":
		return []Transport{poller}
	case "stream":
		return []Transport{streamer}
	case "api":
		// The Data API only, for when the page is not to be relied on.
		return []Transport{streamer, poller}
	default:
		return []Transport{page, streamer, poller}
	}
}

func (i *Ingest) run(ctx context.Context, done chan struct{}) {
	defer close(done)

	candidates := i.Transports()
	delay := 2 * time.Second

	for ctx.Err() == nil {
		videoID, liveChatID, err := i.youtube.ChatTarget(ctx)
		if err != nil {
			i.note(err.Error())
			i.status("waiting", err.Error())
			// Waiting for a broadcast to exist at all: going live is exactly
			// the event that ends this wait.
			if i.waitOrRecheck(ctx, 15*time.Second) {
				return
			}
			continue
		}
		target := Target{VideoID: videoID, LiveChatID: liveChatID}

		i.read.Adopt(target.cursorKey())

		unavailable := false

		for index, transport := range candidates {
			if ctx.Err() != nil {
				return
			}

			i.mu.Lock()
			i.transportInUse = transport.Name()
			i.mu.Unlock()

			name := transport.Name()
			err := transport.Run(ctx, target, i.accept, func() {
				i.status("connected", name)
			})
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

			// Being asked to slow down is answered by slowing down, not by
			// giving up and not by carrying straight on to the next transport
			// -- which would be asking the same server faster.
			var limited *youtube.RateLimitedError
			if errors.As(err, &limited) {
				slog.Info("YouTube asked us to slow down", "reason", err)
				if sleep(ctx, rateLimitBackoff) {
					return
				}
				break
			}

			// Chat being switched off, or the stream having ended, will not
			// start working by asking again. Falling through to the poller
			// only asks the same question a second way and gets the same
			// answer, so stop here and wait for a different broadcast.
			var missing *youtube.ChatUnavailableError
			if errors.As(err, &missing) {
				slog.Info("this broadcast has no live chat to read", "reason", err)
				// Forget the broadcast before waiting. Otherwise the re-check
				// asks about the same cached broadcast, gets the same answer,
				// and switching chat on is never noticed -- nor is ending the
				// stream and starting a new one, which is what actually gives
				// a broadcast a live chat.
				i.youtube.InvalidateBroadcast()
				i.status("unavailable", err.Error())
				unavailable = true
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

		// Re-checked slowly rather than abandoned: she may enable chat, or
		// start a stream that has it, and MikkiLens should pick that up
		// without being restarted.
		if unavailable && i.waitOrRecheck(ctx, chatRecheckInterval) {
			return
		}
	}
}

// ReadCursorFor exposes the saved position, so the reader can record what it
// has spoken. It is fixed at construction, so it needs no lock.
func (i *Ingest) ReadCursorFor() *ReadCursor { return i.read }

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
		// Reconnecting hands back recent history, so this is the ordinary
		// path after a restart, not an unusual one.
		if i.read.AlreadyRead(message) {
			continue
		}
		// After the two checks above, so a message that arrives twice is not
		// counted twice: the recipient's place in the batch is what decides
		// whether their name is read or folded into "and the others".
		i.noteGiftLocked(&message)

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

// gifterNote is what is remembered about one person who gifted memberships:
// their name, how many they bought, and how many of the recipient messages
// have gone past so far.
type gifterNote struct {
	name  string
	total int
	seen  int
}

// noteGiftLocked joins a gifted membership up with the person who paid for it.
//
// YouTube names the giver only on the giving message and identifies them by
// channel id everywhere else, so that message is noted as it goes past and the
// recipients are matched against it. The giving message always arrives first,
// which is what makes this work.
//
// Not knowing is expected rather than exceptional: connecting to a stream
// halfway through a batch means the giving message was never seen. The name is
// left empty then, and the sentence that gets read is written to work either
// way rather than announcing a gift from nobody.
func (i *Ingest) noteGiftLocked(message *Message) {
	switch {
	case message.IsGift && message.AuthorChannelID != "":
		if i.gifters == nil {
			i.gifters = map[string]*gifterNote{}
		}
		// Only ever one entry per giver, so this is bounded by how many people
		// gift rather than by how long the stream runs.
		i.gifters[message.AuthorChannelID] = &gifterNote{
			name: message.Author, total: message.GiftCount,
		}

	case message.IsGiftReceived && message.GifterChannelID != "":
		note := i.gifters[message.GifterChannelID]
		if note == nil {
			return
		}
		note.seen++
		message.GifterName = note.name
		message.GiftIndex = note.seen
		message.GiftTotal = note.total
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
