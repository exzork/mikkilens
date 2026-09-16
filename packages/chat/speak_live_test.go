package chat

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/exzork/mikkilens/packages/audio/feedback"
	"github.com/exzork/mikkilens/packages/core/config"
	"github.com/exzork/mikkilens/packages/core/i18n"
)

// TestSpeakRealChatLive reads a real stream's chat out loud, through the same
// bus, the same voice and the same output device MikkiLens uses.
//
// The printed harness next door shows what would be said; this one says it, so
// a membership landing on the stream can be heard rather than read off a page.
// It loads the real config.toml, so the voice is whichever one she has set.
//
//	MIKKILENS_LIVE=1 MIKKILENS_VIDEO=<id> go test ./packages/chat/ \
//	    -run TestSpeakRealChatLive -v -timeout 15m
//
// MIKKILENS_ONLY=events reads nothing but memberships, gifts and super chats,
// which is what to use when the point is to hear one specific event land in a
// chat that is otherwise busy.
func TestSpeakRealChatLive(t *testing.T) {
	if os.Getenv("MIKKILENS_LIVE") != "1" {
		t.Skip("set MIKKILENS_LIVE=1 to read a real stream's chat aloud")
	}
	video := os.Getenv("MIKKILENS_VIDEO")
	if video == "" {
		t.Skip("set MIKKILENS_VIDEO to the video id of a live stream")
	}
	eventsOnly := os.Getenv("MIKKILENS_ONLY") == "events"

	window := 5 * time.Minute
	if raw := os.Getenv("MIKKILENS_SECONDS"); raw != "" {
		if seconds, err := time.ParseDuration(raw + "s"); err == nil {
			window = seconds
		}
	}

	// Her own config, so this is her voice and her engine rather than a
	// default that would prove nothing about what she will actually hear.
	settings, err := config.Load("")
	if err != nil {
		t.Fatalf("could not load the config: %v", err)
	}
	locale := i18n.Load(settings.Language.Output)

	// Overrides rather than edits to config.toml: trying a voice out should
	// not change the one she is actually streaming with.
	if engine := os.Getenv("MIKKILENS_ENGINE"); engine != "" {
		settings.Speech.Engine = engine
	}
	if rate := os.Getenv("MIKKILENS_RATE"); rate != "" {
		settings.Speech.Rate = rate
		settings.Speech.ChatRate = rate
	}
	if voice := os.Getenv("MIKKILENS_VOICE"); voice != "" {
		settings.Speech.Voice = voice
		// Chat has a voice of its own, and empty means "follow the main one".
		// Left as it was, a chat voice already set would quietly win.
		settings.Speech.ChatVoice = ""
	}
	t.Logf("engine=%q voice=%q chat rate=%q", settings.Speech.Engine, settings.Speech.Voice, settings.Speech.ChatRate)

	bus := feedback.New(settings, locale, nil)
	bus.Start()
	defer bus.Stop()

	// Through a real ingest buffer rather than straight from the transport.
	// The buffer is what numbers a gifted membership -- which recipient this
	// is, and how many were bought -- so a harness that skipped it read all
	// fifty names of a batch the application itself folds into "and the
	// others", and said the reader was at fault.
	ingest := NewIngest(nil, IngestOptions{})
	reader := NewReader(ingest, bus, locale, settings.Chat, nil)

	ctx, cancel := context.WithTimeout(context.Background(), window)
	defer cancel()

	var mu sync.Mutex
	counts := map[string]int{}

	// Fetching and speaking on separate goroutines, the way the application
	// runs them. Done in the delivery callback, chat stopped being fetched for
	// as long as it took to say each message -- so it arrived in late lumps and
	// looked like YouTube being slow, when it was this harness holding the line
	// open and not reading from it.
	speaking := make(chan struct{})
	go func() {
		defer close(speaking)

		idle := time.NewTicker(250 * time.Millisecond)
		defer idle.Stop()

		for ctx.Err() == nil {
			// The same two decisions the reader makes before each message:
			// how fast to read, and what is too old to be worth reading.
			reader.hurry()
			reader.catchUp()

			// Taken through the reader's own cursor rather than a second one
			// kept here. Skipping ahead moves that cursor and drops what it
			// passed, so a harness counting separately went on asking for
			// messages that were no longer there -- which is what left it
			// announcing a skip and then reading nothing ever again.
			message, ok := reader.next()
			if !ok {
				select {
				case <-ctx.Done():
					return
				case <-idle.C:
				}
				continue
			}
			if !reader.shouldRead(message) {
				continue
			}
			if eventsOnly && !message.IsEvent() {
				continue
			}

			kind := "chat"
			switch {
			case message.IsSuperchat:
				kind = "SUPERCHAT"
			case message.IsMember:
				kind = "MEMBERSHIP"
			case message.IsGift:
				kind = "GIFT"
			case message.IsGiftReceived:
				kind = "GIFT RECEIVED"
			}
			mu.Lock()
			counts[kind]++
			mu.Unlock()

			sentence := reader.Render(message)
			fmt.Printf("[%s] %s\n", kind, sentence)
			if message.IsEvent() {
				fmt.Printf("    author=%q member_badge=%v level=%q months=%d\n",
					message.Author, message.AuthorIsMember,
					message.MemberLevel, message.MemberMonths)
			}

			// One at a time with the reader's own gap after it, rather than
			// queueing them: the spacing between messages is half of what is
			// being listened for here, and a probe that ran them together
			// would not be reproducing what she hears.
			spoken := make(chan struct{})
			var once sync.Once
			bus.SayChat(sentence, message.IsPaid(), func(bool) {
				once.Do(func() { close(spoken) })
			})
			select {
			case <-spoken:
			case <-ctx.Done():
				return
			case <-time.After(30 * time.Second):
			}
			time.Sleep(minGap)
		}
	}()

	// The connection does nothing but collect. Everything said happens on the
	// goroutine above, so a long message never holds the fetch open -- which is
	// what had chat arriving in late lumps and looking like YouTube's fault.
	transport := &scrapeTransport{}
	err = transport.Run(ctx, Target{VideoID: video},
		func(batch []Message) { ingest.Accept(batch) },
		func() { t.Logf("connected to the chat page for %s", video) },
	)

	if err != nil && ctx.Err() == nil {
		t.Fatalf("reading chat failed: %v", err)
	}

	// Stop the speaking goroutine and wait for it before draining the queue, so
	// the last thing said is heard in full rather than cut off by the test
	// returning.
	cancel()
	<-speaking
	bus.WaitUntilIdle(30 * time.Second)
	t.Logf("read aloud in %s: %v", window, counts)
}
