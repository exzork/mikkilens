package chat

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/exzork/mikkilens/packages/core/config"
	"github.com/exzork/mikkilens/packages/core/i18n"
	"github.com/exzork/mikkilens/packages/core/intent"
)

// Chat arrives in bursts and is read one message at a time, so a busy minute
// leaves the reading minutes behind -- answering, at length, what was said
// before the thing everyone is talking about now. These pin what catching up
// is allowed to throw away, and what it is not.

// sayingBus records what was said, and nothing else.
type sayingBus struct {
	said  []string
	chat  []string
	hurry float32
}

func (b *sayingBus) SayChat(text string, _ bool, onSpoken func(bool)) {
	b.chat = append(b.chat, text)
	if onSpoken != nil {
		onSpoken(true)
	}
}
func (b *sayingBus) Say(text string, _ intent.Priority) { b.said = append(b.said, text) }
func (b *sayingBus) SayKey(key string, _ intent.Priority, _ ...i18n.Args) {
	b.said = append(b.said, key)
}
func (b *sayingBus) Clear(intent.Priority) int  { return 0 }
func (b *sayingBus) InterruptCurrent()          {}
func (b *sayingBus) SetChatHurry(hurry float32) { b.hurry = hurry }

// waiting builds a reader holding the given messages, none of them read yet.
func waiting(t *testing.T, limit int, messages []Message) (*Reader, *Ingest, *sayingBus) {
	t.Helper()
	ingest := NewIngest(nil, IngestOptions{})
	ingest.Accept(messages)

	settings := config.Default().Chat
	settings.MaxBacklog = limit

	bus := &sayingBus{}
	return NewReader(ingest, bus, i18n.Load("id"), settings, nil), ingest, bus
}

// ordinaryID keeps every generated message distinct. The buffer drops a
// message it has seen before, so a helper that reused ids would deliver
// nothing on its second call and leave a test with nothing to skip.
var ordinaryID int

func ordinary(count int) []Message {
	messages := make([]Message, 0, count)
	for index := 0; index < count; index++ {
		ordinaryID++
		messages = append(messages, Message{
			ID:     fmt.Sprintf("m%d", ordinaryID),
			Author: "orang",
			Text:   "halo",
		})
	}
	return messages
}

func TestCatchingUpDropsTheOldestOrdinaryMessages(t *testing.T) {
	messages := ordinary(16)
	reader, ingest, bus := waiting(t, 5, messages)
	newest := messages[len(messages)-1].ID

	reader.catchUp()

	if got := reader.Backlog(); got != 5 {
		t.Errorf("backlog = %d, want it cut back to the cap of 5", got)
	}
	if got := ingest.Len(); got != 5 {
		t.Errorf("buffer holds %d, want 5", got)
	}
	// The newest are what she wants: the last one typed must still be there.
	pending := reader.PendingMessages()
	if len(pending) == 0 || pending[len(pending)-1].ID != newest {
		t.Errorf("the newest message was dropped: %v", pending)
	}
	if len(bus.said) != 1 || !strings.Contains(bus.said[0], "11") {
		t.Errorf("said %v, want one sentence naming the 11 it skipped", bus.said)
	}
}

// Somebody paid to be heard. However far behind the reading is, that is read.
func TestCatchingUpNeverDropsWhatWasPaidFor(t *testing.T) {
	messages := ordinary(6)
	messages = append(messages, Message{ID: "super", Author: "budi", Text: "semangat",
		IsSuperchat: true, Amount: "Rp50.000"})
	messages = append(messages, ordinary(6)...)
	// The super chat sits in the middle, inside the run that gets dropped.
	reader, ingest, _ := waiting(t, 2, messages)

	reader.catchUp()

	kept := ingest.Snapshot()
	found := false
	for _, message := range kept {
		if message.IsSuperchat {
			found = true
		}
	}
	if !found {
		t.Errorf("the super chat was dropped while catching up: %v", kept)
	}
	// Two ordinary ones plus the super chat that was rescued from the middle.
	if len(kept) != 3 {
		t.Errorf("kept %d messages, want the cap of 2 plus the super chat", len(kept))
	}
}

// A flood is one sentence, not one every few seconds: saying it repeatedly is
// both the noise she was getting away from and time not spent reading chat.
func TestCatchingUpSaysSoOnlyOccasionally(t *testing.T) {
	reader, ingest, bus := waiting(t, 2, ordinary(20))

	reader.catchUp()
	ingest.Accept(ordinary(20))
	reader.catchUp()

	if len(bus.said) != 1 {
		t.Errorf("said %d times, want once: %v", len(bus.said), bus.said)
	}

	// Once the quiet period is over it says so again, because by then it is
	// news rather than a repeat.
	reader.mu.Lock()
	reader.lastCaughtUp = time.Now().Add(-2 * catchUpEvery)
	reader.mu.Unlock()
	ingest.Accept(ordinary(20))
	reader.catchUp()

	if len(bus.said) != 2 {
		t.Errorf("said %d times, want a second one after the quiet period: %v", len(bus.said), bus.said)
	}
}

// Speeding up is the gentler half of catching up, and it comes first: a
// backlog cleared by reading quicker costs nobody their message.
func TestHurryClimbsWithTheBacklogAndThenHolds(t *testing.T) {
	for _, test := range []struct {
		backlog, limit int
		want           float32
	}{
		// Nothing waiting, and a couple in hand, are both the ordinary state
		// of a busy chat and not worth changing her voice over.
		{0, 10, 1},
		{5, 10, 1},
		// The fastest reading and the first dropped message arrive together.
		{10, 10, maxHurry},
		{40, 10, maxHurry},
		// With catching up switched off, reading faster is the only way back.
		{0, 0, 1},
		{40, 0, maxHurry},
	} {
		if got := hurryFor(test.backlog, test.limit); got != test.want {
			t.Errorf("hurryFor(%d, %d) = %v, want %v",
				test.backlog, test.limit, got, test.want)
		}
	}

	// In between it climbs rather than jumping, so the voice does not change
	// pace from one message to the next.
	previous := float32(0)
	for backlog := 5; backlog <= 10; backlog++ {
		got := hurryFor(backlog, 10)
		if got < previous {
			t.Errorf("hurryFor(%d, 10) = %v, below the %v before it", backlog, got, previous)
		}
		if got < 1 || got > maxHurry {
			t.Errorf("hurryFor(%d, 10) = %v, outside 1..%v", backlog, got, maxHurry)
		}
		previous = got
	}
}

// Zero is "read everything, however late it gets", which is what somebody who
// wants every message sets.
func TestCatchingUpOffReadsEverything(t *testing.T) {
	reader, ingest, bus := waiting(t, 0, ordinary(50))

	reader.catchUp()

	if got := ingest.Len(); got != 50 {
		t.Errorf("buffer holds %d, want all 50 kept", got)
	}
	if len(bus.said) != 0 {
		t.Errorf("said %v, want nothing", bus.said)
	}
}
