package chat_test

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	yt "google.golang.org/api/youtube/v3"

	"github.com/exzork/mikkilens/packages/chat"
	"github.com/exzork/mikkilens/packages/core/config"
	"github.com/exzork/mikkilens/packages/core/i18n"
	"github.com/exzork/mikkilens/packages/core/intent"
)

// The behaviour under test is the promise that pausing never loses a message:
// ingestion and playback are separate, so a pause only moves a cursor.

// fakeBus records what would have been said, and completes instantly.
type fakeBus struct {
	mu         sync.Mutex
	locale     *i18n.Locale
	chatSaid   []string
	otherSaid  []string
	cleared    []intent.Priority
	interrupts int
}

func newFakeBus() *fakeBus { return &fakeBus{locale: i18n.Load("id")} }

func (b *fakeBus) SayChat(text string, _ bool, onSpoken func(bool)) {
	b.mu.Lock()
	b.chatSaid = append(b.chatSaid, text)
	b.mu.Unlock()
	if onSpoken != nil {
		onSpoken(true)
	}
}

func (b *fakeBus) Say(text string, _ intent.Priority) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.otherSaid = append(b.otherSaid, text)
}

func (b *fakeBus) SayKey(key string, _ intent.Priority, args ...i18n.Args) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.otherSaid = append(b.otherSaid, b.locale.T(key, args...))
}

func (b *fakeBus) Clear(priority intent.Priority) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.cleared = append(b.cleared, priority)
	return 0
}

func (b *fakeBus) InterruptCurrent() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.interrupts++
}

func (b *fakeBus) chat() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.chatSaid...)
}

func (b *fakeBus) other() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.otherSaid...)
}

func (b *fakeBus) clearOther() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.otherSaid = nil
}

func (b *fakeBus) clearedPriorities() []intent.Priority {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]intent.Priority(nil), b.cleared...)
}

func (b *fakeBus) interruptCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.interrupts
}

func message(index int, adjust ...func(*chat.Message)) chat.Message {
	built := chat.Message{
		ID:         fmt.Sprintf("m%d", index),
		Author:     fmt.Sprintf("user%d", index),
		Text:       fmt.Sprintf("pesan %d", index),
		ReceivedAt: float64(time.Now().UnixNano()) / 1e9,
	}
	for _, change := range adjust {
		change(&built)
	}
	return built
}

func setup(t *testing.T, settings ...config.Chat) (*chat.Ingest, *fakeBus, *chat.Reader) {
	t.Helper()
	chatSettings := config.Default().Chat
	if len(settings) == 1 {
		chatSettings = settings[0]
	}
	ingest := chat.NewIngest(nil, chat.IngestOptions{})
	bus := newFakeBus()
	reader := chat.NewReader(ingest, bus, i18n.Load("id"), chatSettings, nil)
	t.Cleanup(reader.Stop)
	return ingest, bus, reader
}

// waitFor polls until the condition holds or the deadline passes, which keeps
// these tests from depending on exact timing.
func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// -- parsing ------------------------------------------------------------------

func TestParsesAPlainMessage(t *testing.T) {
	parsed, ok := chat.ParseMessage(&yt.LiveChatMessage{
		Id: "abc",
		Snippet: &yt.LiveChatMessageSnippet{
			Type: "textMessageEvent", DisplayMessage: "halo",
			PublishedAt: "2026-01-01T00:00:00Z",
		},
		AuthorDetails: &yt.LiveChatMessageAuthorDetails{DisplayName: "Kaito"},
	})
	if !ok {
		t.Fatal("the message should have parsed")
	}
	if parsed.Author != "Kaito" || parsed.Text != "halo" {
		t.Errorf("parsed = %+v", parsed)
	}
	if parsed.IsSuperchat {
		t.Error("an ordinary message is not a super chat")
	}
}

func TestParsesASuperChatWithItsAmount(t *testing.T) {
	parsed, ok := chat.ParseMessage(&yt.LiveChatMessage{
		Id: "sc",
		Snippet: &yt.LiveChatMessageSnippet{
			Type: "superChatEvent",
			SuperChatDetails: &yt.LiveChatSuperChatDetails{
				AmountDisplayString: "Rp50.000", UserComment: "semangat!",
			},
		},
		AuthorDetails: &yt.LiveChatMessageAuthorDetails{DisplayName: "Rina"},
	})
	if !ok {
		t.Fatal("the super chat should have parsed")
	}
	if !parsed.IsSuperchat || parsed.Amount != "Rp50.000" || parsed.Text != "semangat!" {
		t.Errorf("parsed = %+v", parsed)
	}
}

func TestAMessageWithNoTextIsDropped(t *testing.T) {
	if _, ok := chat.ParseMessage(&yt.LiveChatMessage{
		Id:            "x",
		Snippet:       &yt.LiveChatMessageSnippet{Type: "textMessageEvent"},
		AuthorDetails: &yt.LiveChatMessageAuthorDetails{},
	}); ok {
		t.Error("a message with nothing in it must be dropped")
	}
}

func TestEmoteOnlyMessagesAreRecognised(t *testing.T) {
	if !(chat.Message{Text: "🎉🎉"}).IsEmoteOnly() {
		t.Error("emoji only should be recognised as emote-only")
	}
	if (chat.Message{Text: "halo"}).IsEmoteOnly() {
		t.Error("words are not emote-only")
	}
	if (chat.Message{Text: "こんにちは"}).IsEmoteOnly() {
		t.Error("non-Latin words are still words")
	}
}

// -- ingestion ----------------------------------------------------------------

func TestDuplicateIDsAreIgnored(t *testing.T) {
	ingest, _, _ := setup(t)
	ingest.Accept([]chat.Message{message(1), message(2)})
	ingest.Accept([]chat.Message{message(2), message(3)})

	ids := []string{}
	for _, held := range ingest.Snapshot() {
		ids = append(ids, held.ID)
	}
	if strings.Join(ids, ",") != "m1,m2,m3" {
		t.Errorf("ids = %v", ids)
	}
}

func TestNewMessagesReachTheCallbackOnce(t *testing.T) {
	ingest, _, _ := setup(t)
	seen := []string{}
	ingest.SetOnMessage(func(m chat.Message) { seen = append(seen, m.ID) })

	ingest.Accept([]chat.Message{message(1)})
	ingest.Accept([]chat.Message{message(1), message(2)})

	if strings.Join(seen, ",") != "m1,m2" {
		t.Errorf("seen = %v", seen)
	}
}

// -- the cursor ---------------------------------------------------------------

func TestReaderStartsAtTheEndAndIgnoresHistory(t *testing.T) {
	ingest, bus, reader := setup(t)
	ingest.Accept([]chat.Message{message(1), message(2)})
	reader.Start(true)

	time.Sleep(300 * time.Millisecond)
	if got := bus.chat(); len(got) != 0 {
		t.Errorf("old history must not be read on connect, got %v", got)
	}
	if reader.Backlog() != 0 {
		t.Errorf("backlog = %d", reader.Backlog())
	}
}

func TestMessagesArrivingWhilePlayingAreRead(t *testing.T) {
	ingest, bus, reader := setup(t)
	reader.Start(true)

	ingest.Accept([]chat.Message{message(1), message(2)})
	reader.Notify()

	waitFor(t, "two messages to be read", func() bool { return len(bus.chat()) == 2 })
	if !strings.Contains(bus.chat()[0], "pesan 1") {
		t.Errorf("first message = %q", bus.chat()[0])
	}
}

// TestPauseStopsReadingButIngestKeepsCollecting is the central promise of the
// whole design.
func TestPauseStopsReadingButIngestKeepsCollecting(t *testing.T) {
	ingest, bus, reader := setup(t)
	reader.Start(true)
	time.Sleep(100 * time.Millisecond)

	reader.Pause()
	ingest.Accept([]chat.Message{message(1), message(2), message(3)})
	reader.Notify()
	time.Sleep(400 * time.Millisecond)

	if got := bus.chat(); len(got) != 0 {
		t.Errorf("nothing may be read while paused, got %v", got)
	}
	if ingest.Len() != 3 {
		t.Errorf("everything must still be collected, have %d", ingest.Len())
	}
	if reader.Backlog() != 3 {
		t.Errorf("backlog = %d, want 3", reader.Backlog())
	}
}

func TestResumeReadsTheWholeBacklogInOrder(t *testing.T) {
	ingest, bus, reader := setup(t)
	reader.Start(true)

	reader.Pause()
	ingest.Accept([]chat.Message{message(1), message(2), message(3)})
	reader.Notify()
	time.Sleep(200 * time.Millisecond)
	reader.Resume()

	waitFor(t, "the backlog to be read", func() bool { return len(bus.chat()) == 3 })
	said := bus.chat()
	if !strings.Contains(said[0], "pesan 1") || !strings.Contains(said[2], "pesan 3") {
		t.Errorf("the backlog was read out of order: %v", said)
	}
	if reader.Backlog() != 0 {
		t.Errorf("backlog = %d", reader.Backlog())
	}
}

func TestSkipToNowDropsTheBacklogAndSaysHowMany(t *testing.T) {
	ingest, bus, reader := setup(t)
	reader.Start(true)
	reader.Pause()

	batch := []chat.Message{}
	for index := 0; index < 5; index++ {
		batch = append(batch, message(index))
	}
	ingest.Accept(batch)
	reader.Notify()

	if skipped := reader.SkipToNow(); skipped != 5 {
		t.Errorf("skipped %d, want 5", skipped)
	}
	if reader.Backlog() != 0 {
		t.Errorf("backlog = %d", reader.Backlog())
	}
	if !anyContains(bus.other(), "5") {
		t.Errorf("the count must be spoken, said %v", bus.other())
	}

	reader.Resume()
	time.Sleep(300 * time.Millisecond)
	if got := bus.chat(); len(got) != 0 {
		t.Errorf("skipped messages must not come back, got %v", got)
	}
}

func TestSkipWhenAlreadyCurrentSaysSo(t *testing.T) {
	_, bus, reader := setup(t)
	reader.Start(true)
	reader.SkipToNow()

	said := bus.other()
	if len(said) == 0 || said[len(said)-1] != i18n.Load("id").T("chat.up_to_date") {
		t.Errorf("said = %v", said)
	}
}

func TestReportBacklogCountsPending(t *testing.T) {
	ingest, bus, reader := setup(t)
	reader.Start(true)
	reader.Pause()
	ingest.Accept([]chat.Message{message(1), message(2)})

	if backlog := reader.ReportBacklog(); backlog != 2 {
		t.Errorf("backlog = %d, want 2", backlog)
	}
	if !anyContains(bus.other(), "2") {
		t.Errorf("the count must be spoken, said %v", bus.other())
	}
}

func TestPausingTwiceIsReportedNotSilent(t *testing.T) {
	_, bus, reader := setup(t)
	reader.Start(true)
	reader.Pause()
	bus.clearOther()

	if reader.Pause() {
		t.Error("a second pause should report that it changed nothing")
	}
	if len(bus.other()) == 0 {
		t.Error("a redundant pause must still say something")
	}
}

func TestPauseClearsQueuedChatAndCutsTheCurrentLine(t *testing.T) {
	_, bus, reader := setup(t)
	reader.Start(true)
	reader.Pause()

	found := false
	for _, priority := range bus.clearedPriorities() {
		if priority == intent.PriorityChat {
			found = true
		}
	}
	if !found {
		t.Error("pausing must clear the queued chat")
	}
	if bus.interruptCount() < 1 {
		t.Error("pausing must cut off the line in progress")
	}
}

// -- filtering ----------------------------------------------------------------

func TestMutedUsersAreNeverRead(t *testing.T) {
	settings := config.Default().Chat
	settings.MutedUsers = []string{"spammer"}
	ingest, bus, reader := setup(t, settings)
	reader.Start(true)

	ingest.Accept([]chat.Message{
		message(1, func(m *chat.Message) { m.Author = "Spammer" }),
		message(2, func(m *chat.Message) { m.Author = "Kaito" }),
	})
	reader.Notify()

	waitFor(t, "one message to be read", func() bool { return len(bus.chat()) == 1 })
	time.Sleep(200 * time.Millisecond)
	said := bus.chat()
	if len(said) != 1 || !strings.Contains(said[0], "Kaito") {
		t.Errorf("said = %v", said)
	}
}

func TestEmoteOnlyMessagesAreSkippedWhenConfigured(t *testing.T) {
	ingest, bus, reader := setup(t)
	reader.Start(true)

	ingest.Accept([]chat.Message{
		message(1, func(m *chat.Message) { m.Text = "🎉" }),
		message(2, func(m *chat.Message) { m.Text = "halo" }),
	})
	reader.Notify()

	waitFor(t, "the readable message", func() bool { return len(bus.chat()) == 1 })
	time.Sleep(200 * time.Millisecond)
	if said := bus.chat(); len(said) != 1 || !strings.Contains(said[0], "halo") {
		t.Errorf("said = %v", said)
	}
}

func TestRepeatedIdenticalMessagesCollapse(t *testing.T) {
	ingest, bus, reader := setup(t)
	reader.Start(true)

	ingest.Accept([]chat.Message{
		message(1, func(m *chat.Message) { m.Text = "sama" }),
		message(2, func(m *chat.Message) { m.Text = "sama" }),
	})
	reader.Notify()

	waitFor(t, "the first message", func() bool { return len(bus.chat()) == 1 })
	time.Sleep(300 * time.Millisecond)
	if said := bus.chat(); len(said) != 1 {
		t.Errorf("the duplicate should have collapsed, said %v", said)
	}
}

func TestALongMessageIsShortened(t *testing.T) {
	settings := config.Default().Chat
	settings.MaxMessageChars = 20
	_, _, reader := setup(t, settings)

	rendered := reader.Render(message(1, func(m *chat.Message) {
		m.Text = strings.Repeat("x", 100)
	}))
	if !strings.Contains(rendered, "…") {
		t.Errorf("a long message should be cut off: %q", rendered)
	}
	if len([]rune(rendered)) >= 90 {
		t.Errorf("rendered is %d runes: %q", len([]rune(rendered)), rendered)
	}
}

// TestSuperChatsAreReadBeforeOrdinaryMessages: someone paid for that one.
func TestSuperChatsAreReadBeforeOrdinaryMessages(t *testing.T) {
	ingest, bus, reader := setup(t)
	reader.Start(true)
	reader.Pause()

	ingest.Accept([]chat.Message{
		message(1, func(m *chat.Message) { m.Text = "biasa satu" }),
		message(2, func(m *chat.Message) {
			m.Text = "terima kasih"
			m.IsSuperchat = true
			m.Amount = "Rp50.000"
		}),
		message(3, func(m *chat.Message) { m.Text = "biasa dua" }),
	})
	reader.Resume()

	waitFor(t, "the first message to be read", func() bool { return len(bus.chat()) >= 1 })
	if !strings.Contains(bus.chat()[0], "Rp50.000") {
		t.Errorf("a super chat must jump the queue, first was %q", bus.chat()[0])
	}
}

// -- transport selection ------------------------------------------------------

// TestAutoPrefersStreamingAndKeepsPollingAsAFallback matters for quota:
// polling a four hour stream would exhaust the daily allowance on its own.
func TestAutoPrefersStreamingAndKeepsPollingAsAFallback(t *testing.T) {
	ingest := chat.NewIngest(nil, chat.IngestOptions{Transport: "auto"})
	names := []string{}
	for _, transport := range ingest.Transports() {
		names = append(names, transport.Name())
	}
	if strings.Join(names, ",") != "stream,poll" {
		t.Errorf("transports = %v, want stream then poll", names)
	}
}

func TestForcingPollingSkipsTheStreamingTransport(t *testing.T) {
	ingest := chat.NewIngest(nil, chat.IngestOptions{Transport: "poll"})
	transports := ingest.Transports()
	if len(transports) != 1 || transports[0].Name() != "poll" {
		t.Errorf("transports = %v", transports)
	}
}

func TestPollingNeverGoesFasterThanItsFloor(t *testing.T) {
	if chat.MinPollInterval < 5*time.Second {
		t.Error("polling faster than five seconds would burn the daily quota on a long stream")
	}
}

func anyContains(values []string, needle string) bool {
	for _, value := range values {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}
