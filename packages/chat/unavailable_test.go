package chat

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/exzork/mikkilens/packages/controllers/youtube"
)

// A broadcast with live chat switched off is a permanent condition, not a
// blip. Treating it as a blip is what produced an endless "chat connected /
// chat disconnected" every couple of seconds -- spoken aloud, with no way for
// her to make it stop.

// fakeTransport fails however the test asks it to, and records whether it
// claimed to be ready.
type fakeTransport struct {
	name       string
	err        error
	readyFirst bool
	runs       int
}

func (f *fakeTransport) Name() string { return f.name }

func (f *fakeTransport) Run(
	_ context.Context, _ Target, _ func([]Message), ready func(),
) error {
	f.runs++
	if f.readyFirst {
		ready()
	}
	return f.err
}

func TestConnectedIsNotAnnouncedUntilChatActuallyWorks(t *testing.T) {
	transport := &fakeTransport{name: "fake", err: errors.New("nope")}

	var announced []string
	ingest := NewIngest(nil, IngestOptions{
		OnStatus: func(state, _ string) { announced = append(announced, state) },
	})

	// Run the transport the way the ingest loop does.
	_ = transport.Run(context.Background(), Target{LiveChatID: "chat-1"}, ingest.accept, func() {
		ingest.status("connected", transport.Name())
	})

	for _, state := range announced {
		if state == "connected" {
			t.Fatal("a transport that failed must never announce that chat connected")
		}
	}
}

func TestATransportThatWorksDoesAnnounceItself(t *testing.T) {
	transport := &fakeTransport{name: "fake", readyFirst: true, err: errors.New("later")}

	var announced []string
	ingest := NewIngest(nil, IngestOptions{
		OnStatus: func(state, _ string) { announced = append(announced, state) },
	})

	_ = transport.Run(context.Background(), Target{LiveChatID: "chat-1"}, ingest.accept, func() {
		ingest.status("connected", transport.Name())
	})

	if len(announced) != 1 || announced[0] != "connected" {
		t.Errorf("announced %v, want exactly one connected", announced)
	}
}

// The whole point of the separate error type: falling through to the poller
// asks the same question a second way and gets the same answer, and retrying
// on a two second backoff turns it into a stream of announcements.
func TestChatBeingSwitchedOffIsNotRetriedLikeABlip(t *testing.T) {
	err := error(&youtube.ChatUnavailableError{Reason: "Live chat is not enabled"})

	var missing *youtube.ChatUnavailableError
	if !errors.As(err, &missing) {
		t.Fatal("the ingest loop identifies this with errors.As")
	}
	if _, blip := err.(*youtube.QuotaExhaustedError); blip {
		t.Error("chat being off is not a quota problem")
	}
}

// It must not be abandoned either: she may switch chat on, or start a stream
// that has it, and that should be picked up without a restart.
func TestABroadcastWithNoChatIsCheckedAgainLater(t *testing.T) {
	if chatRecheckInterval <= 0 {
		t.Fatal("a broadcast with no chat must still be re-checked")
	}
	if chatRecheckInterval < time.Minute {
		t.Errorf("re-checking every %v is close enough to hammering it",
			chatRecheckInterval)
	}
	if chatRecheckInterval > 10*time.Minute {
		t.Errorf("re-checking every %v means switching chat on goes unnoticed "+
			"for most of a stream", chatRecheckInterval)
	}
}

// -- waking up ----------------------------------------------------------------

// After being told this broadcast has no chat, the loop waits minutes. Going
// live again has to cut that short, or she starts a stream with chat switched
// on and hears nothing until the timer happens to expire.
func TestGoingLiveCutsTheWaitShort(t *testing.T) {
	ingest := NewIngest(nil, IngestOptions{})
	ingest.Recheck()

	started := time.Now()
	stop := ingest.waitOrRecheck(context.Background(), time.Hour)

	if stop {
		t.Fatal("a nudge must resume the loop, not stop it")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Errorf("waited %v; a pending nudge must be acted on at once", elapsed)
	}
}

func TestTheWaitStillEndsOnItsOwn(t *testing.T) {
	ingest := NewIngest(nil, IngestOptions{})

	if stop := ingest.waitOrRecheck(context.Background(), 10*time.Millisecond); stop {
		t.Error("a wait that simply finished must not stop the loop")
	}
}

func TestShuttingDownStopsTheLoopEvenMidWait(t *testing.T) {
	ingest := NewIngest(nil, IngestOptions{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if stop := ingest.waitOrRecheck(ctx, time.Hour); !stop {
		t.Error("a cancelled context must stop the loop rather than wait an hour")
	}
}

// A nudge that arrives while the loop is busy must not be lost, and nudging
// repeatedly must never block the caller -- it is called from an OBS event
// handler, which cannot afford to wait on anything.
func TestNudgingIsRememberedAndNeverBlocks(t *testing.T) {
	ingest := NewIngest(nil, IngestOptions{})

	done := make(chan struct{})
	go func() {
		for range 100 {
			ingest.Recheck()
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Recheck blocked; an OBS event handler cannot afford to wait")
	}

	if stop := ingest.waitOrRecheck(context.Background(), time.Hour); stop {
		t.Error("the remembered nudge must still wake the loop")
	}
}
