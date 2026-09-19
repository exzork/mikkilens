package youtube

import (
	"testing"

	yt "google.golang.org/api/youtube/v3"
)

// Chat was read from whichever broadcast YouTube listed first. "Active" is not
// only the stream she is on -- a broadcast whose stream dropped stays live with
// auto-stop off, and one in preview is active too -- so that was sometimes the
// wrong chat, read in full while hers went unheard.

func broadcast(id, lifecycle, started, scheduled string) *yt.LiveBroadcast {
	return &yt.LiveBroadcast{
		Id:     id,
		Status: &yt.LiveBroadcastStatus{LifeCycleStatus: lifecycle},
		Snippet: &yt.LiveBroadcastSnippet{
			ActualStartTime:    started,
			ScheduledStartTime: scheduled,
		},
	}
}

func TestTheLatestLiveBroadcastIsTheOneShesOn(t *testing.T) {
	dropped := broadcast("dropped", "live", "2026-09-18T12:00:00Z", "")
	current := broadcast("current", "live", "2026-09-19T12:00:00Z", "")

	for _, order := range [][]*yt.LiveBroadcast{{dropped, current}, {current, dropped}} {
		if got := pickBroadcast(order); got.Id != "current" {
			t.Errorf("picked %q, want the one that went live last", got.Id)
		}
	}
}

func TestLiveBeatsAPreview(t *testing.T) {
	preview := broadcast("preview", "testing", "", "")
	live := broadcast("live", "live", "2026-09-19T12:00:00Z", "")

	if got := pickBroadcast([]*yt.LiveBroadcast{preview, live}); got.Id != "live" {
		t.Errorf("picked %q, want the one on air", got.Id)
	}
}

// Going on air right now has no start time yet, and is newer than anything
// that has one.
func TestABroadcastStartingNowIsTheNewest(t *testing.T) {
	older := broadcast("older", "live", "2026-09-19T12:00:00Z", "")
	starting := broadcast("starting", "liveStarting", "", "")

	if got := pickBroadcast([]*yt.LiveBroadcast{starting, older}); got.Id != "starting" {
		t.Errorf("picked %q, want the one starting now", got.Id)
	}
}

func TestOfTheScheduledOnesTheNextDue(t *testing.T) {
	later := broadcast("later", "ready", "", "2026-09-25T12:00:00Z")
	next := broadcast("next", "ready", "", "2026-09-20T12:00:00Z")

	if got := pickBroadcast([]*yt.LiveBroadcast{later, next}); got.Id != "next" {
		t.Errorf("picked %q, want the one due soonest", got.Id)
	}
}

func TestNothingListedIsNothingPicked(t *testing.T) {
	if got := pickBroadcast(nil); got != nil {
		t.Errorf("picked %q from nothing", got.Id)
	}
}

// A scheduled broadcast has a chat of its own -- the waiting room -- and
// reading it while she is live elsewhere is reading the wrong chat.
func TestOnlyABroadcastOnAirHasChatToRead(t *testing.T) {
	for status, want := range map[string]bool{
		"live": true, "liveStarting": true, "active": true,
		"ready": false, "created": false, "testing": false, "upcoming": false,
	} {
		if got := (&Broadcast{Status: status}).IsLive(); got != want {
			t.Errorf("IsLive(%q) = %v, want %v", status, got, want)
		}
	}
}
