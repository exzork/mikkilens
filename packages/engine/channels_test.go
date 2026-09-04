package engine

import (
	"testing"
	"time"

	"github.com/exzork/mikkilens/packages/core/config"
)

// withChannels builds the smallest engine these tests need. Nothing is started:
// resolving a name and remembering an expected profile touch only the settings
// and the lock, which is the point of keeping them free of the controllers.
func withChannels(channels ...config.Channel) *Engine {
	settings := config.Default()
	settings.YouTube.Channels = channels
	return &Engine{settings: settings}
}

func TestResolveChannelMatchesNameOrProfile(t *testing.T) {
	engine := withChannels(
		config.Channel{Name: "main", ChannelID: "UCmain", Profile: "Main"},
		config.Channel{Name: "music", ChannelID: "UCmusic", Profile: "Music Review"},
	)

	cases := map[string]string{
		"music":        "UCmusic",
		"Music":        "UCmusic",
		"music review": "UCmusic", // the OBS profile name, which she may say instead
		"musik":        "UCmusic", // heard through Indonesian speech recognition
		"main":         "UCmain",
	}
	for spoken, want := range cases {
		found, ok := engine.ResolveChannel(spoken)
		if !ok || found.ChannelID != want {
			t.Errorf("ResolveChannel(%q) = %+v, %v; want %s", spoken, found, ok, want)
		}
	}
}

func TestResolveChannelRefusesNonsense(t *testing.T) {
	engine := withChannels(
		config.Channel{Name: "main", ChannelID: "UCmain", Profile: "Main"},
		config.Channel{Name: "music", ChannelID: "UCmusic", Profile: "Music Review"},
	)

	// Guessing here is worse than not answering: the wrong guess sends the next
	// broadcast to the wrong channel.
	for _, spoken := range []string{"", "   ", "photography"} {
		if found, ok := engine.ResolveChannel(spoken); ok {
			t.Errorf("ResolveChannel(%q) matched %+v", spoken, found)
		}
	}

	if _, ok := withChannels().ResolveChannel("music"); ok {
		t.Error("resolved a channel with none configured")
	}
}

// Switching a profile makes OBS announce the change back, and that
// announcement is indistinguishable from her picking it from the menu herself.
// Without this, every deliberate switch would trigger a second one on top of
// itself.
func TestExpectedProfileIsConsumedOnce(t *testing.T) {
	engine := withChannels()

	engine.expectProfile("Music Review")
	if !engine.wasExpected("music review") {
		t.Error("the echo of our own switch was not recognised")
	}
	// Once only. A second change to the same profile is hers, and has to be
	// followed.
	if engine.wasExpected("Music Review") {
		t.Error("the expected profile was not forgotten after being used")
	}
}

func TestUnexpectedProfileIsFollowed(t *testing.T) {
	engine := withChannels()

	if engine.wasExpected("Main") {
		t.Error("a profile change nobody asked for was treated as our own")
	}

	engine.expectProfile("Main")
	if engine.wasExpected("Music Review") {
		t.Error("a different profile matched the expected one")
	}

	// A stale expectation must not swallow a change she makes much later.
	engine.expectProfile("Main")
	engine.mu.Lock()
	engine.expectedAt = time.Now().Add(-2 * expectedProfileGrace)
	engine.mu.Unlock()
	if engine.wasExpected("Main") {
		t.Error("a stale expectation swallowed a profile change")
	}
}
