package feedback_test

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/exzork/mikkilens/packages/audio/devices"
	"github.com/exzork/mikkilens/packages/audio/earcons"
	"github.com/exzork/mikkilens/packages/audio/feedback"
	"github.com/exzork/mikkilens/packages/audio/tts"
	"github.com/exzork/mikkilens/packages/core/config"
	"github.com/exzork/mikkilens/packages/core/i18n"
)

// These run without audio hardware: synthesis and playback are replaced with
// instant fakes, so what is under test is the queue logic, which is the part
// that has to be right.

// fakePlayer records what was played and honours Stop like the real speaker.
type fakePlayer struct {
	mu          sync.Mutex
	playSeconds float64
	played      []string
	interrupted []string
	interrupt   chan struct{}
	started     chan struct{}
	startOnce   sync.Once
}

func newFakePlayer(playSeconds float64) *fakePlayer {
	return &fakePlayer{
		playSeconds: playSeconds,
		interrupt:   make(chan struct{}),
		started:     make(chan struct{}),
	}
}

func (p *fakePlayer) SetDevice(*devices.Device) {}

func (p *fakePlayer) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	select {
	case <-p.interrupt:
	default:
		close(p.interrupt)
	}
}

func (p *fakePlayer) Play(audio tts.Audio) (bool, error) {
	p.mu.Lock()
	p.interrupt = make(chan struct{})
	interrupt := p.interrupt
	duration := time.Duration(p.playSeconds * float64(time.Second))
	p.mu.Unlock()

	p.startOnce.Do(func() { close(p.started) })

	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-interrupt:
		p.mu.Lock()
		p.interrupted = append(p.interrupted, audio.Text)
		p.mu.Unlock()
		return false, nil
	case <-timer.C:
		p.mu.Lock()
		p.played = append(p.played, audio.Text)
		p.mu.Unlock()
		return true, nil
	}
}

func (p *fakePlayer) playedTexts() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.played...)
}

func (p *fakePlayer) interruptedTexts() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.interrupted...)
}

func (p *fakePlayer) waitForStart(t *testing.T) {
	t.Helper()
	select {
	case <-p.started:
	case <-time.After(2 * time.Second):
		t.Fatal("playback never started")
	}
}

func fakeSynthesize(_ context.Context, text string, _ tts.Options) (tts.Audio, error) {
	return tts.Audio{
		Samples: make([]float32, 10), SampleRate: 48000, Channels: 1, Text: text,
	}, nil
}

func newBus(t *testing.T, playSeconds float64) (*feedback.Bus, *fakePlayer) {
	t.Helper()
	player := newFakePlayer(playSeconds)
	settings := config.Default()
	settings.Speech.EarconVolume = 0 // silence the tones; the queue is what matters
	bus := feedback.NewWith(settings, i18n.Load("id"), player, fakeSynthesize)
	t.Cleanup(bus.Stop)
	return bus, player
}

func drain(t *testing.T, bus *feedback.Bus) {
	t.Helper()
	if !bus.WaitUntilIdle(5 * time.Second) {
		t.Fatal("the bus never went idle")
	}
}

// -- ordering -----------------------------------------------------------------

func TestSpeaksInPriorityOrderRegardlessOfArrival(t *testing.T) {
	// Queue backwards, least important first, all before the worker starts, so
	// the result cannot depend on how fast the worker is.
	bus, player := newBus(t, 0.02)
	bus.Say("chat one", feedback.Chat)
	bus.Say("result", feedback.Result)
	bus.Say("confirm", feedback.Confirm)
	bus.Say("error", feedback.Error)

	bus.Start()
	drain(t, bus)

	want := []string{"error", "confirm", "result", "chat one"}
	if got := player.playedTexts(); !reflect.DeepEqual(got, want) {
		t.Errorf("played %v, want %v", got, want)
	}
}

func TestEqualPriorityKeepsArrivalOrder(t *testing.T) {
	bus, player := newBus(t, 0.01)
	want := []string{}
	for index := 0; index < 5; index++ {
		text := "chat " + string(rune('0'+index))
		want = append(want, text)
		bus.Say(text, feedback.Chat)
	}

	bus.Start()
	drain(t, bus)

	if got := player.playedTexts(); !reflect.DeepEqual(got, want) {
		t.Errorf("played %v, want %v", got, want)
	}
}

// -- preemption ---------------------------------------------------------------

func TestErrorInterruptsChatAndChatIsReRead(t *testing.T) {
	bus, player := newBus(t, 0.6)
	bus.Start()
	bus.SayChat("a long chat message", false, nil)
	player.waitForStart(t)

	bus.Error("obs.not_responding")
	drain(t, bus)

	if got := player.interruptedTexts(); !reflect.DeepEqual(got, []string{"a long chat message"}) {
		t.Errorf("interrupted %v, want the chat message", got)
	}
	played := player.playedTexts()
	if len(played) == 0 || played[0] != bus.Locale().T("obs.not_responding") {
		t.Errorf("the error should have been said first, got %v", played)
	}
	if !contains(played, "a long chat message") {
		t.Error("the interrupted message must be re-read, not dropped")
	}
}

func TestResultDoesNotInterruptAConfirmationPrompt(t *testing.T) {
	bus, player := newBus(t, 0.4)
	bus.Start()
	bus.Say("stop the stream?", feedback.Confirm)
	player.waitForStart(t)

	bus.Say("some status", feedback.Result)
	drain(t, bus)

	if got := player.interruptedTexts(); len(got) != 0 {
		t.Errorf("a confirmation prompt must never be cut off, interrupted %v", got)
	}
	want := []string{"stop the stream?", "some status"}
	if got := player.playedTexts(); !reflect.DeepEqual(got, want) {
		t.Errorf("played %v, want %v", got, want)
	}
}

func TestClearDropsOnlyTheNamedPriority(t *testing.T) {
	bus, player := newBus(t, 0.5)
	bus.Start()
	bus.Say("blocking", feedback.Confirm)
	player.waitForStart(t)

	for index := 0; index < 4; index++ {
		bus.Say("chat "+string(rune('0'+index)), feedback.Chat)
	}
	bus.Say("keep me", feedback.Result)

	if removed := bus.Clear(feedback.Chat); removed != 4 {
		t.Errorf("cleared %d, want 4", removed)
	}
	drain(t, bus)

	want := []string{"blocking", "keep me"}
	if got := player.playedTexts(); !reflect.DeepEqual(got, want) {
		t.Errorf("played %v, want %v", got, want)
	}
}

// -- refusals and history -----------------------------------------------------

func TestEmptyTextIsRefusedRatherThanQueued(t *testing.T) {
	bus, player := newBus(t, 0.01)
	bus.Say("   ", feedback.Result)
	bus.Say("", feedback.Error)

	bus.Start()
	drain(t, bus)

	if got := player.playedTexts(); len(got) != 0 {
		t.Errorf("played %v, want nothing", got)
	}
}

func TestHistoryRecordsCompletion(t *testing.T) {
	bus, _ := newBus(t, 0.01)
	bus.Say("hello", feedback.Result)
	bus.Start()
	drain(t, bus)

	history := bus.History()
	if len(history) != 1 {
		t.Fatalf("history = %+v", history)
	}
	if history[0].Text != "hello" || !history[0].Completed {
		t.Errorf("history[0] = %+v", history[0])
	}
}

func TestOnSpokenFiresWhenTheUtteranceFinishes(t *testing.T) {
	bus, _ := newBus(t, 0.01)
	done := make(chan bool, 1)
	bus.Enqueue(feedback.Utterance{
		Text:     "did it finish",
		Priority: feedback.Result,
		OnSpoken: func(completed bool) { done <- completed },
	})
	bus.Start()

	select {
	case completed := <-done:
		if !completed {
			t.Error("the utterance should have completed")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("OnSpoken never fired")
	}
}

// -- earcons ------------------------------------------------------------------

// TestEarconCacheSurvivesRepeatedPlays is a regression: the Python version
// crashed on the second play of any given tone, because it truth-tested a
// cached array. Rendering twice must hit the cache and return the same bytes.
func TestEarconCacheSurvivesRepeatedPlays(t *testing.T) {
	earcons.ClearCache()
	for _, name := range earcons.Names() {
		first, err := earcons.Render(name, 0.2)
		if err != nil {
			t.Fatalf("Render(%q): %v", name, err)
		}
		second, err := earcons.Render(name, 0.2)
		if err != nil {
			t.Fatalf("Render(%q) again: %v", name, err)
		}
		if len(second) == 0 {
			t.Errorf("%q rendered nothing", name)
		}
		if &first[0] != &second[0] {
			t.Errorf("the second call to %q missed the cache", name)
		}
	}
}

// TestEveryEarconIsDistinct: they are told apart by ear alone, so none of them
// may be a duplicate of another.
func TestEveryEarconIsDistinct(t *testing.T) {
	seen := map[string]string{}
	for _, name := range earcons.Names() {
		wave, err := earcons.Render(name, 0.2)
		if err != nil {
			t.Fatalf("Render(%q): %v", name, err)
		}
		key := fingerprint(wave)
		if other, clash := seen[key]; clash {
			t.Errorf("%q and %q sound identical", name, other)
		}
		seen[key] = name
	}
}

func TestUnknownEarconIsReportedNotFatal(t *testing.T) {
	if _, err := earcons.Render("nope", 0.2); err == nil {
		t.Error("an unknown earcon must be reported")
	}
	bus, _ := newBus(t, 0.01)
	bus.Earcon("nope") // must not panic
}

// -- helpers ------------------------------------------------------------------

func contains(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

func fingerprint(wave []float32) string {
	// Length plus a coarse sum is enough to tell seven short tones apart.
	total := 0.0
	for _, sample := range wave {
		total += float64(sample) * float64(sample)
	}
	return string(rune(len(wave))) + ":" + formatFloat(total)
}

func formatFloat(value float64) string {
	return time.Duration(value * 1e6).String()
}
