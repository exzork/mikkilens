package feedback_test

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/exzork/mikkilens/packages/audio/feedback"
	"github.com/exzork/mikkilens/packages/audio/tts"
	"github.com/exzork/mikkilens/packages/core/config"
	"github.com/exzork/mikkilens/packages/core/i18n"
)

// Chat used to be rendered only once the message before it had finished, so
// every second the voice took was heard as silence between two people.
// PrepareChat renders the next one while this one plays. What these pin is
// that the work moves and nothing else does: the same words, in the same
// order, and a guess that turns out wrong costs nothing but the rendering.

// countingSynthesizer is slow enough for rendering to be seen overlapping
// playback, and remembers what it was asked for and what it finished.
type countingSynthesizer struct {
	mu        sync.Mutex
	took      time.Duration
	asked     []string
	cancelled []string
}

func (s *countingSynthesizer) synthesize(ctx context.Context, text string, _ tts.Options) (tts.Audio, error) {
	s.mu.Lock()
	s.asked = append(s.asked, text)
	s.mu.Unlock()

	select {
	case <-time.After(s.took):
	case <-ctx.Done():
		s.mu.Lock()
		s.cancelled = append(s.cancelled, text)
		s.mu.Unlock()
		return tts.Audio{}, ctx.Err()
	}
	return tts.Audio{Samples: make([]float32, 10), SampleRate: 48000, Channels: 1, Text: text}, nil
}

func (s *countingSynthesizer) askedFor() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.asked...)
}

func (s *countingSynthesizer) cancelledFor() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.cancelled...)
}

func newAheadBus(t *testing.T, playSeconds float64, took time.Duration) (*feedback.Bus, *fakePlayer, *countingSynthesizer) {
	t.Helper()
	player := newFakePlayer(playSeconds)
	voice := &countingSynthesizer{took: took}
	settings := config.Default()
	settings.Speech.EarconVolume = 0
	bus := feedback.NewWith(settings, i18n.Load("id"), player, voice.synthesize)
	t.Cleanup(bus.Stop)
	return bus, player, voice
}

// The whole point: the second line is rendered once, during the first, and is
// ready the moment it is asked for.
func TestTheNextChatLineIsRenderedWhileThisOneIsHeard(t *testing.T) {
	bus, player, voice := newAheadBus(t, 0.4, 150*time.Millisecond)
	bus.Start()

	first := make(chan bool, 1)
	bus.SayChat("first", false, func(completed bool) { first <- completed })
	bus.PrepareChat("second", false)

	// Rendered while the first is still being heard, not after.
	eventually(t, 2*time.Second, "the second line was never started", func() bool {
		return contains(voice.askedFor(), "second")
	})
	if played := player.playedTexts(); contains(played, "first") {
		t.Fatal("the second line only started rendering after the first had finished")
	}

	<-first
	started := time.Now()
	bus.SayChat("second", false, nil)
	drain(t, bus)

	if got, want := voice.askedFor(), []string{"first", "second"}; !reflect.DeepEqual(got, want) {
		t.Errorf("rendered %v, want each line once: %v", got, want)
	}
	if got, want := player.playedTexts(), []string{"first", "second"}; !reflect.DeepEqual(got, want) {
		t.Errorf("played %v, want %v", got, want)
	}
	// The second line's own playback is 0.4 s; anything much past that is
	// the 150 ms render being paid again.
	if waited := time.Since(started); waited > 520*time.Millisecond {
		t.Errorf("the second line took %s to be heard; it should already have been rendered", waited)
	}
}

// A guess that turns out wrong -- the message was skipped, or a super chat
// came first -- is thrown away, and what is actually said is rendered and
// heard as if nothing had been prepared.
func TestAWrongGuessIsDroppedAndTheRealLineIsSpoken(t *testing.T) {
	bus, player, voice := newAheadBus(t, 0.05, 300*time.Millisecond)
	bus.Start()

	bus.PrepareChat("never said", false)
	eventually(t, time.Second, "the guess was never started", func() bool {
		return contains(voice.askedFor(), "never said")
	})
	bus.SayChat("actually said", false, nil)
	drain(t, bus)

	if got, want := player.playedTexts(), []string{"actually said"}; !reflect.DeepEqual(got, want) {
		t.Errorf("played %v, want %v", got, want)
	}
	eventually(t, time.Second, "the wrong guess was left running", func() bool {
		return contains(voice.cancelledFor(), "never said")
	})
}

// An error arriving while the next chat line is being rendered is not held up
// behind a render it has no use for.
func TestSomethingMoreImportantCancelsTheRenderingAhead(t *testing.T) {
	bus, player, voice := newAheadBus(t, 0.05, 300*time.Millisecond)
	bus.Start()

	bus.PrepareChat("chat", false)
	eventually(t, time.Second, "the chat line was never started", func() bool {
		return contains(voice.askedFor(), "chat")
	})
	bus.Error("chat.paused")
	drain(t, bus)

	if played := player.playedTexts(); len(played) != 1 || contains(played, "chat") {
		t.Errorf("played %v, want only the error", played)
	}
	eventually(t, time.Second, "the chat line kept rendering after the error took its place", func() bool {
		return contains(voice.cancelledFor(), "chat")
	})
}
