package chat

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/exzork/mikkilens/packages/core/config"
	"github.com/exzork/mikkilens/packages/core/i18n"
)

// TestPrintRealChatLive reads a real stream's chat and prints what would be
// said, so the reading can be checked against a chat somebody is actually
// typing in. It talks to YouTube, so it is skipped unless MIKKILENS_LIVE=1.
//
//	MIKKILENS_LIVE=1 MIKKILENS_VIDEO=<id> go test ./packages/chat/ \
//	    -run TestPrintRealChatLive -v -timeout 5m
//
// It asserts almost nothing on purpose: what a live chat contains is not ours
// to decide, and a test that failed because nobody said anything for a minute
// would be noise. What it is for is the column on the right -- the sentence
// the voice would read -- next to the message that produced it.
func TestPrintRealChatLive(t *testing.T) {
	if os.Getenv("MIKKILENS_LIVE") != "1" {
		t.Skip("set MIKKILENS_LIVE=1 to read a real stream's chat")
	}
	video := os.Getenv("MIKKILENS_VIDEO")
	if video == "" {
		t.Skip("set MIKKILENS_VIDEO to the video id of a live stream")
	}

	window := 90 * time.Second
	if raw := os.Getenv("MIKKILENS_SECONDS"); raw != "" {
		if seconds, err := time.ParseDuration(raw + "s"); err == nil {
			window = seconds
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), window)
	defer cancel()

	locale := i18n.Load("id")
	settings := config.Default().Chat
	reader := NewReader(nil, nil, locale, settings, nil)

	transport := &scrapeTransport{}
	seen := 0
	events := map[string]int{}

	err := transport.Run(ctx, Target{VideoID: video},
		func(batch []Message) {
			for _, message := range batch {
				seen++
				kind := "chat"
				switch {
				case message.IsSuperchat:
					kind = "superchat"
				case message.IsMember:
					kind = "member"
				case message.IsGift:
					kind = "gift"
				case message.IsGiftReceived:
					kind = "gift-received"
				}
				events[kind]++

				badges := ""
				if message.AuthorIsMember {
					badges += " [member]"
				}
				if message.IsModerator {
					badges += " [mod]"
				}
				if message.IsOwner {
					badges += " [owner]"
				}

				fmt.Printf("%-13s %s%s\n", kind, message.Author, badges)
				fmt.Printf("%-13s typed: %s\n", "", message.Text)
				fmt.Printf("%-13s says:  %s\n", "", reader.Render(message))
				if !reader.shouldRead(message) {
					fmt.Printf("%-13s (filtered out, not read)\n", "")
				}
				fmt.Println()
			}
		},
		func() { t.Logf("connected to the chat page for %s", video) },
	)

	if err != nil && ctx.Err() == nil {
		t.Fatalf("reading chat failed: %v", err)
	}
	t.Logf("read %d messages in %s: %v", seen, window, events)
	if seen == 0 {
		t.Log("nothing was said in that window -- the stream may be quiet, " +
			"or chat may be off")
	}
}
