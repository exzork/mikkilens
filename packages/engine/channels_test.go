package engine

import (
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/exzork/mikkilens/packages/audio/feedback"
	"github.com/exzork/mikkilens/packages/controllers/youtube"
	"github.com/exzork/mikkilens/packages/core/config"
	"github.com/exzork/mikkilens/packages/core/i18n"
	"github.com/exzork/mikkilens/packages/core/paths"
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

// -- disconnecting one channel ------------------------------------------------

// The Disconnect in the YouTube box signs out of everything. This is the other
// one: the channel she no longer streams to goes, and the one she does stays.

func withConnectedChannels(t *testing.T, channels ...config.Channel) *Engine {
	t.Helper()
	t.Setenv("MIKKILENS_SILENT", "1")
	paths.SetRoot(t.TempDir())

	settings := config.Default()
	settings.YouTube.Enabled = true
	settings.YouTube.Channels = channels
	if len(channels) > 0 {
		settings.YouTube.Active = channels[0].ChannelID
	}

	engine := &Engine{settings: settings, locale: i18n.Load("id")}
	engine.bus = feedback.NewWith(settings, engine.locale, silentSpeaker{}, silentVoice)
	t.Cleanup(engine.bus.Stop)

	for _, channel := range channels {
		if err := youtube.SaveAccount(youtube.Account{
			ChannelID:    channel.ChannelID,
			ChannelTitle: channel.Name,
			Token:        &oauth2.Token{RefreshToken: "refresh-" + channel.ChannelID},
		}); err != nil {
			t.Fatalf("could not write a sign-in: %v", err)
		}
	}
	return engine
}

func TestDisconnectingOneChannelLeavesTheOthersSignedIn(t *testing.T) {
	engine := withConnectedChannels(t,
		config.Channel{Name: "utama", ChannelID: "UCmain", Profile: "Main"},
		config.Channel{Name: "musik", ChannelID: "UCmusic", Profile: "Music"},
	)

	if err := engine.DisconnectChannel("UCmusic"); err != nil {
		t.Fatalf("DisconnectChannel: %v", err)
	}

	settings := engine.Config()
	if len(settings.YouTube.Channels) != 1 || settings.YouTube.Channels[0].ChannelID != "UCmain" {
		t.Errorf("channels are %+v, want only the main one", settings.YouTube.Channels)
	}
	if _, ok := youtube.LoadAccount("UCmusic"); ok {
		t.Error("the sign-in is still on disk")
	}
	if _, ok := youtube.LoadAccount("UCmain"); !ok {
		t.Error("the channel she still uses was signed out as well")
	}
	if !settings.YouTube.Enabled {
		t.Error("YouTube was switched off while a channel is still connected")
	}
}

// The active channel is the one whose chat is being read. Removing it must
// leave nothing pointing at it, or the next start goes looking for a sign-in
// that is not there.
func TestDisconnectingTheActiveChannelClearsIt(t *testing.T) {
	engine := withConnectedChannels(t,
		config.Channel{Name: "utama", ChannelID: "UCmain", Profile: "Main"},
		config.Channel{Name: "musik", ChannelID: "UCmusic", Profile: "Music"},
	)

	if err := engine.DisconnectChannel("UCmain"); err != nil {
		t.Fatalf("DisconnectChannel: %v", err)
	}
	if active := engine.Config().YouTube.Active; active != "" {
		t.Errorf("the active channel is still %q", active)
	}
}

// The last one out lands on the same state as the Disconnect button, or the
// settings page would say YouTube is on with nothing to be on as.
func TestDisconnectingTheLastChannelSwitchesYouTubeOff(t *testing.T) {
	engine := withConnectedChannels(t,
		config.Channel{Name: "utama", ChannelID: "UCmain", Profile: "Main"},
	)

	if err := engine.DisconnectChannel("UCmain"); err != nil {
		t.Fatalf("DisconnectChannel: %v", err)
	}

	settings := engine.Config()
	if len(settings.YouTube.Channels) != 0 {
		t.Errorf("channels are %+v, want none", settings.YouTube.Channels)
	}
	if settings.YouTube.Enabled {
		t.Error("YouTube is still on with nothing signed in")
	}
	if youtube.HasAccounts() {
		t.Error("a sign-in survived the last channel being removed")
	}
}

// A sign-in with no pairing in config is the one most likely to be here by
// mistake: connected once, never bound to a profile. It has to be removable.
func TestASignInWithNoPairingCanStillBeRemoved(t *testing.T) {
	engine := withConnectedChannels(t)
	if err := youtube.SaveAccount(youtube.Account{
		ChannelID: "UCstray", ChannelTitle: "Stray", Token: &oauth2.Token{RefreshToken: "x"},
	}); err != nil {
		t.Fatal(err)
	}

	if err := engine.DisconnectChannel("UCstray"); err != nil {
		t.Fatalf("DisconnectChannel: %v", err)
	}
	if _, ok := youtube.LoadAccount("UCstray"); ok {
		t.Error("the stray sign-in is still on disk")
	}
}

func TestDisconnectingNothingIsRefused(t *testing.T) {
	engine := withConnectedChannels(t)
	if err := engine.DisconnectChannel("   "); err == nil {
		t.Error("an empty channel id was accepted")
	}
}
