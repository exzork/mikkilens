package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/exzork/mikkilens/packages/core/config"
)

func twoChannels() config.YouTube {
	return config.YouTube{
		Channels: []config.Channel{
			{Name: "main", ChannelID: "UCmain", Profile: "Main"},
			{Name: "music", ChannelID: "UCmusic", Profile: "Music Review",
				SceneCollection: "Music Review"},
		},
	}
}

func TestChannelForProfileIgnoresCaseAndSpace(t *testing.T) {
	settings := twoChannels()

	// The profile name comes from OBS and the one it is compared against was
	// typed into config.toml by hand. "music review" not matching "Music
	// Review" would be a switch that silently does nothing.
	for _, spelling := range []string{"Music Review", "music review", "  MUSIC REVIEW  "} {
		found, ok := settings.ChannelForProfile(spelling)
		if !ok || found.ChannelID != "UCmusic" {
			t.Errorf("ChannelForProfile(%q) = %+v, %v", spelling, found, ok)
		}
	}

	if _, ok := settings.ChannelForProfile("Recording Only"); ok {
		t.Error("an unbound profile matched a channel")
	}
	if _, ok := settings.ChannelForProfile(""); ok {
		t.Error("an empty profile name matched a channel")
	}
}

func TestFindChannelRefusesAnEmptyID(t *testing.T) {
	settings := twoChannels()

	// A sign-in that has not been identified yet has no id, and it must not
	// match a configured channel that happens to have none either -- that
	// would point her at the wrong channel's chat.
	settings.Channels = append(settings.Channels, config.Channel{Name: "half-set-up"})
	if _, ok := settings.FindChannel(""); ok {
		t.Error("an empty channel id matched a channel")
	}
	if found, ok := settings.FindChannel("UCmain"); !ok || found.Name != "main" {
		t.Errorf("FindChannel(UCmain) = %+v, %v", found, ok)
	}
}

// The channel list changes on every switch, so it must not read as a settings
// change that tears the sign-in and the chat connection down and rebuilds them.
func TestSameConnectionIgnoresTheChannelList(t *testing.T) {
	before := config.YouTube{Enabled: true, Transport: "auto", QuotaBudget: 10000}
	after := before
	after.Channels = twoChannels().Channels
	after.Active = "UCmusic"

	if !before.SameConnection(after) {
		t.Error("switching channel counted as a change to the YouTube connection")
	}

	after.Transport = "poll"
	if before.SameConnection(after) {
		t.Error("changing the chat transport did not count as a change")
	}
}

func TestChannelNamedFallsBackToSomethingTrue(t *testing.T) {
	cases := []struct {
		channel config.Channel
		want    string
	}{
		{config.Channel{Name: "music", Profile: "Music Review"}, "music"},
		{config.Channel{Profile: "Music Review", ChannelID: "UCmusic"}, "Music Review"},
		{config.Channel{ChannelID: "UCmusic"}, "UCmusic"},
	}
	for _, one := range cases {
		if got := one.channel.Named(); got != one.want {
			t.Errorf("Named() = %q, want %q", got, one.want)
		}
	}
}

// The pairing has to survive a round trip through config.toml, which is where
// it lives between runs.
func TestChannelsSurviveSaveAndLoad(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "config.toml")

	settings := config.Default()
	settings.YouTube.Active = "UCmusic"
	settings.YouTube.Channels = twoChannels().Channels

	if _, err := settings.Save(path); err != nil {
		t.Fatal(err)
	}

	// [[youtube.channels]] rather than a flat key, which is what makes it
	// readable by hand -- the file is hers to edit.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(string(raw), "[[youtube.channels]]") {
		t.Errorf("channels were not written as a table array:\n%s", raw)
	}

	loaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.YouTube.Active != "UCmusic" {
		t.Errorf("active channel = %q", loaded.YouTube.Active)
	}
	if len(loaded.YouTube.Channels) != 2 {
		t.Fatalf("expected 2 channels, got %+v", loaded.YouTube.Channels)
	}
	if loaded.YouTube.Channels[1].SceneCollection != "Music Review" {
		t.Errorf("scene collection was lost: %+v", loaded.YouTube.Channels[1])
	}
}

func contains(haystack, needle string) bool {
	for index := 0; index+len(needle) <= len(haystack); index++ {
		if haystack[index:index+len(needle)] == needle {
			return true
		}
	}
	return false
}

// The settings page saves whole sections at a time, and it has no idea the
// channel list exists. Dropping the pairings every time somebody changed the
// chat transport would be a page that quietly undoes the setup it took a
// browser consent per channel to make.
func TestSavingOtherSettingsKeepsTheChannels(t *testing.T) {
	settings := config.Default()
	settings.YouTube.Active = "UCmusic"
	settings.YouTube.Channels = twoChannels().Channels

	// Exactly what the API does: whole config out as a map, one key changed,
	// and back in again.
	document := settings.ToMap()
	youtube, ok := document["youtube"].(map[string]any)
	if !ok {
		t.Fatalf("no [youtube] section in the map: %+v", document["youtube"])
	}
	youtube["transport"] = "poll"
	document["youtube"] = youtube

	updated := config.FromMap(document)
	if updated.YouTube.Transport != "poll" {
		t.Errorf("the change did not apply: %q", updated.YouTube.Transport)
	}
	if len(updated.YouTube.Channels) != 2 {
		t.Fatalf("the channels were dropped: %+v", updated.YouTube.Channels)
	}
	if updated.YouTube.Channels[1].Profile != "Music Review" {
		t.Errorf("a channel came back wrong: %+v", updated.YouTube.Channels[1])
	}
	if updated.YouTube.Active != "UCmusic" {
		t.Errorf("the active channel was dropped: %q", updated.YouTube.Active)
	}
}
