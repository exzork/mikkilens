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
	bus.Say("donation", feedback.Donation)
	bus.Say("result", feedback.Result)
	bus.Say("confirm", feedback.Confirm)
	bus.Say("error", feedback.Error)

	bus.Start()
	drain(t, bus)

	want := []string{"error", "confirm", "result", "donation", "chat one"}
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

// TestADonationJumpsTheChatBacklog: a donation read twenty messages late has
// already stopped being worth reading out.
func TestADonationJumpsTheChatBacklog(t *testing.T) {
	bus, player := newBus(t, 0.01)
	for index := 0; index < 5; index++ {
		bus.SayChat("chat "+string(rune('0'+index)), false, nil)
	}
	bus.SayDonation("thanks for the stream", nil)

	bus.Start()
	drain(t, bus)

	played := player.playedTexts()
	if len(played) == 0 || played[0] != "thanks for the stream" {
		t.Errorf("the donation should have been read first, got %v", played)
	}
}

// TestADonationInterruptsChatAndTheChatIsReRead: preempting the reader must
// not cost her the message it cut off.
func TestADonationInterruptsChatAndTheChatIsReRead(t *testing.T) {
	bus, player := newBus(t, 0.6)
	bus.Start()
	bus.SayChat("a long chat message", false, nil)
	player.waitForStart(t)

	bus.SayDonation("thanks for the stream", nil)
	drain(t, bus)

	if got := player.interruptedTexts(); !reflect.DeepEqual(got, []string{"a long chat message"}) {
		t.Errorf("interrupted %v, want the chat message", got)
	}
	played := player.playedTexts()
	if len(played) == 0 || played[0] != "thanks for the stream" {
		t.Errorf("the donation should have been said first, got %v", played)
	}
	if !contains(played, "a long chat message") {
		t.Error("the interrupted message must be re-read, not dropped")
	}
}

// TestADonationDoesNotInterruptTheAppsOwnVoice: someone paying does not get to
// talk over an error or an open question.
func TestADonationDoesNotInterruptTheAppsOwnVoice(t *testing.T) {
	bus, player := newBus(t, 0.4)
	bus.Start()
	bus.Say("stop the stream?", feedback.Confirm)
	player.waitForStart(t)

	bus.SayDonation("thanks for the stream", nil)
	drain(t, bus)

	if got := player.interruptedTexts(); len(got) != 0 {
		t.Errorf("a confirmation prompt must never be cut off, interrupted %v", got)
	}
	want := []string{"stop the stream?", "thanks for the stream"}
	if got := player.playedTexts(); !reflect.DeepEqual(got, want) {
		t.Errorf("played %v, want %v", got, want)
	}
}

// TestAnErrorInterruptsADonationAndTheDonationIsReRead: the one thing above a
// donation still preempts it, and still owes it a second reading.
func TestAnErrorInterruptsADonationAndTheDonationIsReRead(t *testing.T) {
	bus, player := newBus(t, 0.6)
	bus.Start()
	bus.SayDonation("thanks for the stream", nil)
	player.waitForStart(t)

	bus.Error("obs.not_responding")
	drain(t, bus)

	if got := player.interruptedTexts(); !reflect.DeepEqual(got, []string{"thanks for the stream"}) {
		t.Errorf("interrupted %v, want the donation", got)
	}
	if !contains(player.playedTexts(), "thanks for the stream") {
		t.Error("a message someone paid for must never be dropped")
	}
}

// TestSkippingChatLeavesDonationsQueued: "skip to now" throws away the chat
// backlog, and a donation is not part of it.
func TestSkippingChatLeavesDonationsQueued(t *testing.T) {
	bus, player := newBus(t, 0.01)
	bus.SayChat("chat one", false, nil)
	bus.SayChat("chat two", false, nil)
	bus.SayDonation("thanks for the stream", nil)

	if dropped := bus.Clear(feedback.Chat); dropped != 2 {
		t.Errorf("dropped %d chat messages, want 2", dropped)
	}

	bus.Start()
	drain(t, bus)

	if got := player.playedTexts(); !reflect.DeepEqual(got, []string{"thanks for the stream"}) {
		t.Errorf("played %v, want only the donation", got)
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

// -- voice per priority -------------------------------------------------------

// Chat is read at its own volume and rate: it runs for hours under the stream,
// where a confirmation is a short interruption meant to be heard over it.
func TestChatIsSynthesizedAtTheChatVolumeAndConfirmationsAtTheSpeechVolume(t *testing.T) {
	var mu sync.Mutex
	spoken := map[string]tts.Options{}
	recording := func(_ context.Context, text string, options tts.Options) (tts.Audio, error) {
		mu.Lock()
		spoken[text] = options
		mu.Unlock()
		return tts.Audio{
			Samples: make([]float32, 10), SampleRate: 48000, Channels: 1, Text: text,
		}, nil
	}

	settings := config.Default()
	settings.Speech.EarconVolume = 0
	settings.Speech.Volume = "+10%"
	settings.Speech.ChatVolume = "-30%"

	player := newFakePlayer(0.01)
	bus := feedback.NewWith(settings, i18n.Load("id"), player, recording)
	t.Cleanup(bus.Stop)

	bus.SayChat("a chat message", false, nil)
	bus.Say("stop the stream?", feedback.Confirm)
	bus.Start()
	drain(t, bus)

	mu.Lock()
	defer mu.Unlock()
	if got := spoken["a chat message"].Volume; got != "-30%" {
		t.Errorf("chat volume = %q, want the chat volume -30%%", got)
	}
	if got := spoken["stop the stream?"].Volume; got != "+10%" {
		t.Errorf("confirmation volume = %q, want the speech volume +10%%", got)
	}
}

// An unset chat volume is the ordinary case: it means "the same as everything
// else", not silence.
func TestChatFallsBackToTheSpeechVolumeWhenUnset(t *testing.T) {
	var mu sync.Mutex
	var volume string
	recording := func(_ context.Context, text string, options tts.Options) (tts.Audio, error) {
		mu.Lock()
		volume = options.Volume
		mu.Unlock()
		return tts.Audio{
			Samples: make([]float32, 10), SampleRate: 48000, Channels: 1, Text: text,
		}, nil
	}

	settings := config.Default()
	settings.Speech.EarconVolume = 0
	settings.Speech.Volume = "-20%"
	settings.Speech.ChatVolume = ""

	player := newFakePlayer(0.01)
	bus := feedback.NewWith(settings, i18n.Load("id"), player, recording)
	t.Cleanup(bus.Stop)

	bus.SayChat("a chat message", false, nil)
	bus.Start()
	drain(t, bus)

	mu.Lock()
	defer mu.Unlock()
	if volume != "-20%" {
		t.Errorf("chat volume = %q, want the speech volume -20%%", volume)
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

// -- the donation hold ---------------------------------------------------------

// While a donation alert is on screen Tako is reading it out in its own voice.
// Chat has to wait, and it has to wait without losing anything: these are the
// two halves of that promise.

// eventually waits for a condition, so a slow machine fails the test late
// rather than falsely.
func eventually(t *testing.T, within time.Duration, why string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", why)
}

func TestHeldChatIsNotSpokenUntilTheHoldExpires(t *testing.T) {
	bus, player := newBus(t, 0.02)
	bus.Start()

	bus.HoldChat(time.Now().Add(500 * time.Millisecond))
	bus.SayChat("a viewer says hello", false, nil)

	time.Sleep(200 * time.Millisecond)
	if got := player.playedTexts(); len(got) != 0 {
		t.Fatalf("chat was read over the donation alert: %v", got)
	}

	eventually(t, 3*time.Second, "chat to be read once the alert is over", func() bool {
		return contains(player.playedTexts(), "a viewer says hello")
	})
}

func TestHoldSilencesChatOnlyAndNotHerOwnVoice(t *testing.T) {
	bus, player := newBus(t, 0.02)
	bus.Start()

	bus.HoldChat(time.Now().Add(600 * time.Millisecond))
	bus.SayChat("a viewer says hello", false, nil)
	bus.Error("obs.not_responding")

	// A microphone or OBS failure is about her, not about the stream. Waiting
	// for a donation to finish before telling her would be the wrong trade.
	eventually(t, 2*time.Second, "the error to be said through the hold", func() bool {
		return contains(player.playedTexts(), bus.Locale().T("obs.not_responding"))
	})
	if contains(player.playedTexts(), "a viewer says hello") {
		t.Error("chat was read during the hold")
	}
}

func TestHoldCutsOffAChatMessageAndReadsItAgainInFull(t *testing.T) {
	bus, player := newBus(t, 0.5)
	bus.Start()
	bus.SayChat("a long chat message", false, nil)
	player.waitForStart(t)

	// The donation lands mid-sentence, which is the case that matters: the
	// alternative to cutting it off is talking over the alert.
	bus.HoldChat(time.Now().Add(400 * time.Millisecond))

	eventually(t, 2*time.Second, "the chat message to be cut off", func() bool {
		return contains(player.interruptedTexts(), "a long chat message")
	})
	eventually(t, 4*time.Second, "the cut-off message to be read again", func() bool {
		return contains(player.playedTexts(), "a long chat message")
	})
}

func TestReleaseChatLiftsTheHoldEarly(t *testing.T) {
	bus, player := newBus(t, 0.02)
	bus.Start()

	bus.HoldChat(time.Now().Add(30 * time.Second))
	bus.SayChat("a viewer says hello", false, nil)

	time.Sleep(100 * time.Millisecond)
	if got := player.playedTexts(); len(got) != 0 {
		t.Fatalf("chat was read while held: %v", got)
	}

	// Nothing should be able to leave chat silent for half a minute because a
	// watcher stopped with a hold still booked.
	bus.ReleaseChat()
	eventually(t, 2*time.Second, "chat to be read after the hold was lifted", func() bool {
		return contains(player.playedTexts(), "a viewer says hello")
	})
}

func TestHoldOnlyEverExtends(t *testing.T) {
	bus, _ := newBus(t, 0.02)
	bus.Start()

	far := time.Now().Add(10 * time.Second)
	bus.HoldChat(far)
	bus.HoldChat(time.Now().Add(time.Second))

	// A second, shorter donation must not shorten the quiet the first one
	// booked, or the tail of a long alert gets talked over.
	held, until := bus.ChatHeld()
	if !held {
		t.Fatal("chat should still be held")
	}
	if until.Before(far.Add(-50 * time.Millisecond)) {
		t.Errorf("a shorter hold cut the longer one short: until %v, want about %v", until, far)
	}
}

// TestAHoldArrivingDuringSynthesisStillSilencesTheMessage is the regression
// test for a donation being talked over.
//
// A chat message that has left the queue but is still being synthesized is
// past the gate and not yet playing, so neither protects it: interrupting a
// player that has not started is forgotten the moment it does start. Chat is
// always synthesized fresh, so there is a window like this on every single
// message, and a donation landing in it was read straight over the alert.
func TestAHoldArrivingDuringSynthesisStillSilencesTheMessage(t *testing.T) {
	synthesizing := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once

	slow := func(ctx context.Context, text string, options tts.Options) (tts.Audio, error) {
		once.Do(func() { close(synthesizing) })
		<-release
		return fakeSynthesize(ctx, text, options)
	}

	player := newFakePlayer(0.02)
	settings := config.Default()
	settings.Speech.EarconVolume = 0
	bus := feedback.NewWith(settings, i18n.Load("id"), player, slow)
	t.Cleanup(bus.Stop)

	bus.Start()
	bus.SayChat("a viewer says hello", false, nil)

	// Wait until it is being synthesized, which is the moment the old code
	// could not be interrupted in.
	select {
	case <-synthesizing:
	case <-time.After(2 * time.Second):
		t.Fatal("synthesis never started")
	}

	bus.HoldChat(time.Now().Add(600 * time.Millisecond))
	close(release)

	time.Sleep(250 * time.Millisecond)
	if got := player.playedTexts(); contains(got, "a viewer says hello") {
		t.Fatal("chat was read over the donation alert")
	}

	// And it is not lost: held back, then read once the alert is over.
	eventually(t, 3*time.Second, "the held message to be read after the alert", func() bool {
		return contains(player.playedTexts(), "a viewer says hello")
	})
}

// -- the paid tier -------------------------------------------------------------

// A super chat and a gifted membership sit in the same tier as a Tako or
// Trakteer alert, because they are the same thing: somebody paid.

func TestAPaidChatMessageJumpsTheBacklog(t *testing.T) {
	bus, player := newBus(t, 0.02)
	for index := range 5 {
		bus.SayChat("ordinary "+string(rune('0'+index)), false, nil)
	}
	bus.SayChat("someone paid", true, nil)

	bus.Start()
	drain(t, bus)

	// Heard while it is still current rather than five messages later.
	played := player.playedTexts()
	if len(played) == 0 || played[0] != "someone paid" {
		t.Errorf("the paid message should have been read first, got %v", played)
	}
}

func TestAPaidChatMessageIsStillSilencedByADonationAlert(t *testing.T) {
	// The point of moving super chats up a tier was not to let them talk over
	// a Tako or Trakteer alert. Two paid messages at once is the exact thing
	// the hold exists to prevent.
	bus, player := newBus(t, 0.02)
	bus.Start()

	bus.HoldChat(time.Now().Add(500 * time.Millisecond))
	bus.SayChat("someone paid", true, nil)

	time.Sleep(200 * time.Millisecond)
	if got := player.playedTexts(); len(got) != 0 {
		t.Fatalf("a super chat was read over the alert: %v", got)
	}

	eventually(t, 3*time.Second, "the paid message once the alert is over", func() bool {
		return contains(player.playedTexts(), "someone paid")
	})
}

func TestHerOwnVoiceStillSpeaksThroughAHold(t *testing.T) {
	// Widening the hold to cover the paid tier must not have caught anything
	// above it: an error or an open question is about her, not the stream.
	bus, player := newBus(t, 0.02)
	bus.Start()

	bus.HoldChat(time.Now().Add(600 * time.Millisecond))
	bus.SayChat("someone paid", true, nil)

	// The error goes on first on purpose. Enqueued the other way round it
	// preempts whatever is already being said, and a result is dropped rather
	// than requeued when that happens -- which is correct, and would make this
	// test fail for a reason that has nothing to do with the hold.
	bus.Error("obs.not_responding")
	bus.Say("some status", feedback.Result)

	eventually(t, 2*time.Second, "her own voice to speak through the hold", func() bool {
		played := player.playedTexts()
		return contains(played, "some status") &&
			contains(played, bus.Locale().T("obs.not_responding"))
	})
	if contains(player.playedTexts(), "someone paid") {
		t.Error("the paid message was read during the hold")
	}
}

func TestTheDonationBeingAnnouncedSpeaksThroughItsOwnHold(t *testing.T) {
	// When she reads a Tako or Trakteer donation herself, the hold is on
	// because of that donation. Holding the announcement too would leave it
	// spoken after the alert had left the screen.
	bus, player := newBus(t, 0.02)
	bus.Start()

	bus.HoldChat(time.Now().Add(600 * time.Millisecond))
	bus.SayChat("a viewer says hello", false, nil)
	bus.SayDonation("Donation from Budi, Rp10.000", nil)

	eventually(t, 2*time.Second, "the donation to be announced during the hold", func() bool {
		return contains(player.playedTexts(), "Donation from Budi, Rp10.000")
	})
	if contains(player.playedTexts(), "a viewer says hello") {
		t.Error("chat was read during the hold")
	}
}
