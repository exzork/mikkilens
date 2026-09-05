package feedback_test

import (
	"testing"
	"time"
)

// The mute key: one press to stop the voice in her ear, one to give it back.
//
// The thing under test is that nothing is lost. Muting to take a phone call or
// to let a guest talk must not cost her the chat that arrived while it was
// quiet, because the alternative -- reading it later -- is what makes muting
// something she is willing to do at all.

func TestMutingStopsChatFromBeingRead(t *testing.T) {
	bus, player := newBus(t, 0.02)
	bus.Start()

	bus.SetChatMuted(true)
	bus.SayChat("a viewer says hello", false, nil)

	time.Sleep(200 * time.Millisecond)
	if got := player.playedTexts(); len(got) != 0 {
		t.Fatalf("chat was read while muted: %v", got)
	}
}

func TestUnmutingReadsWhatArrivedWhileItWasQuiet(t *testing.T) {
	bus, player := newBus(t, 0.02)
	bus.Start()

	bus.SetChatMuted(true)
	bus.SayChat("first", false, nil)
	bus.SayChat("second", false, nil)

	time.Sleep(150 * time.Millisecond)
	if got := player.playedTexts(); len(got) != 0 {
		t.Fatalf("chat was read while muted: %v", got)
	}

	// Held rather than dropped is the whole design: two people said something
	// while she was busy, and both are still owed an answer.
	bus.SetChatMuted(false)
	eventually(t, 3*time.Second, "the muted backlog to be read", func() bool {
		played := player.playedTexts()
		return contains(played, "first") && contains(played, "second")
	})
}

// The moment she reaches for the key is the moment a voice is already talking,
// and a mute that waits for the current message to finish is not a mute.
func TestMutingCutsOffTheMessageBeingReadAndKeepsIt(t *testing.T) {
	bus, player := newBus(t, 0.5)
	bus.Start()
	bus.SayChat("a long chat message", false, nil)
	player.waitForStart(t)

	bus.SetChatMuted(true)
	eventually(t, 2*time.Second, "the chat message to be cut off", func() bool {
		return contains(player.interruptedTexts(), "a long chat message")
	})

	time.Sleep(200 * time.Millisecond)
	if contains(player.playedTexts(), "a long chat message") {
		t.Fatal("the message carried on being read after the mute")
	}

	// And it is read from the start once she gives chat back, rather than
	// resuming halfway or being lost to the interruption.
	bus.SetChatMuted(false)
	eventually(t, 4*time.Second, "the cut-off message to be read again", func() bool {
		return contains(player.playedTexts(), "a long chat message")
	})
}

// Muting chat must never mute MikkiLens. An error, a question she is being
// asked, an answer she asked for: those are about her rather than about the
// stream, and a mute that swallowed "OBS is not responding" would be a way to
// go off the air quietly.
func TestMutingSilencesChatOnlyAndNotHerOwnVoice(t *testing.T) {
	bus, player := newBus(t, 0.02)
	bus.Start()

	bus.SetChatMuted(true)
	bus.SayChat("a viewer says hello", false, nil)
	bus.Error("obs.not_responding")

	eventually(t, 2*time.Second, "the error to be said through the mute", func() bool {
		return contains(player.playedTexts(), bus.Locale().T("obs.not_responding"))
	})
	if contains(player.playedTexts(), "a viewer says hello") {
		t.Error("chat was read while muted")
	}
}

// Somebody paid to be heard, so it is held rather than dropped -- the same
// trade the donation hold makes, for the same reason.
func TestMutingHoldsPaidMessagesAndGivesThemBack(t *testing.T) {
	bus, player := newBus(t, 0.02)
	bus.Start()

	bus.SetChatMuted(true)
	bus.SayChat("a super chat", true, nil)
	bus.SayDonation("a donation", nil)

	time.Sleep(200 * time.Millisecond)
	if got := player.playedTexts(); len(got) != 0 {
		t.Fatalf("paid messages were read while muted: %v", got)
	}

	bus.SetChatMuted(false)
	eventually(t, 3*time.Second, "the paid messages to be read once unmuted", func() bool {
		played := player.playedTexts()
		return contains(played, "a super chat") && contains(played, "a donation")
	})
}

// One key, no way to see which way it is set, so the key has to be the answer
// to both questions.
func TestToggleReportsWhichWayItWent(t *testing.T) {
	bus, _ := newBus(t, 0.02)
	bus.Start()

	if bus.ChatMuted() {
		t.Fatal("chat started out muted")
	}
	if muted := bus.ToggleChatMuted(); !muted || !bus.ChatMuted() {
		t.Fatalf("the first press reported muted=%v", muted)
	}
	if muted := bus.ToggleChatMuted(); muted || bus.ChatMuted() {
		t.Fatalf("the second press reported muted=%v", muted)
	}
}

// The mute and the donation hold are separate answers to separate problems,
// and lifting one must not lift the other. Releasing the hold when an alert
// finishes cannot be allowed to un-mute chat she muted on purpose.
func TestTheMuteOutlastsADonationHold(t *testing.T) {
	bus, player := newBus(t, 0.02)
	bus.Start()

	bus.SetChatMuted(true)
	bus.HoldChat(time.Now().Add(200 * time.Millisecond))
	bus.SayChat("a viewer says hello", false, nil)

	bus.ReleaseChat()
	time.Sleep(300 * time.Millisecond)
	if got := player.playedTexts(); len(got) != 0 {
		t.Fatalf("the hold ending un-muted chat: %v", got)
	}

	bus.SetChatMuted(false)
	eventually(t, 2*time.Second, "chat once it is un-muted", func() bool {
		return contains(player.playedTexts(), "a viewer says hello")
	})
}

// And the other way round: un-muting must not talk over an alert that is still
// on screen.
func TestUnmutingDoesNotBreakADonationHold(t *testing.T) {
	bus, player := newBus(t, 0.02)
	bus.Start()

	bus.SetChatMuted(true)
	bus.SayChat("a viewer says hello", false, nil)
	bus.HoldChat(time.Now().Add(600 * time.Millisecond))

	bus.SetChatMuted(false)
	time.Sleep(200 * time.Millisecond)
	if got := player.playedTexts(); len(got) != 0 {
		t.Fatalf("chat was read over the alert: %v", got)
	}

	eventually(t, 3*time.Second, "chat once the alert is over", func() bool {
		return contains(player.playedTexts(), "a viewer says hello")
	})
}

// Setting it to what it already is must not cut off the message being read.
// The settings page writes this on every save.
func TestMutingWhenAlreadyMutedChangesNothing(t *testing.T) {
	bus, player := newBus(t, 0.5)
	bus.Start()
	bus.SayChat("a long chat message", false, nil)
	player.waitForStart(t)

	bus.SetChatMuted(false)
	time.Sleep(200 * time.Millisecond)
	if contains(player.interruptedTexts(), "a long chat message") {
		t.Fatal("un-muting an un-muted bus cut off the message being read")
	}
}
