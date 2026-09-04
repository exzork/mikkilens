package youtube

import (
	"testing"
	"time"
)

// The cached broadcast used to have no expiry at all, so the first answer of
// the session was kept forever. Everything on it goes stale: the title she
// changed in Studio, whether the broadcast has a live chat, and the broadcast
// id itself once she ends one stream and starts another.

func TestAFreshlyCachedBroadcastIsReused(t *testing.T) {
	isolate(t)
	controller := &Controller{Quota: NewLedger(10000, 80)}
	controller.broadcast = &Broadcast{ID: "abc", Title: "Main Minecraft"}
	controller.broadcastFetched = time.Now()

	// Reusing it is the point of the cache: a voice command must not wait on
	// a round trip that was made a second ago.
	cached, fresh := controller.cachedBroadcast()
	if !fresh || cached == nil || cached.ID != "abc" {
		t.Error("a broadcast fetched just now must be reused")
	}
}

func TestAStaleBroadcastIsNotReused(t *testing.T) {
	isolate(t)
	controller := &Controller{Quota: NewLedger(10000, 80)}
	controller.broadcast = &Broadcast{ID: "abc", Title: "an old title"}
	controller.broadcastFetched = time.Now().Add(-2 * broadcastTTL)

	if _, fresh := controller.cachedBroadcast(); fresh {
		t.Error("a broadcast this old must be looked up again, or she is told " +
			"a title she has already changed")
	}
}

// A broadcast cached before it was ever stamped -- which is what an
// uninitialised time value means -- must not be treated as current.
func TestABroadcastWithNoFetchTimeIsNotTrusted(t *testing.T) {
	isolate(t)
	controller := &Controller{Quota: NewLedger(10000, 80)}
	controller.broadcast = &Broadcast{ID: "abc"}

	if _, fresh := controller.cachedBroadcast(); fresh {
		t.Error("an unstamped broadcast must not count as fresh")
	}
}

// This is the one that mattered: reading chat failed because the broadcast has
// none, so the cached answer is known to be wrong. Waiting and asking the same
// cached question again would never notice her switching chat on, or ending
// the stream and starting one that has it.
func TestInvalidatingForgetsTheBroadcastEntirely(t *testing.T) {
	isolate(t)
	controller := &Controller{Quota: NewLedger(10000, 80)}
	controller.broadcast = &Broadcast{ID: "abc"}
	controller.broadcastFetched = time.Now()

	controller.InvalidateBroadcast()

	if cached, fresh := controller.cachedBroadcast(); fresh || cached != nil {
		t.Error("the broadcast must be forgotten, not merely aged")
	}
}

// The TTL is a judgement call, so it is worth stating what it is balancing:
// liveBroadcasts.list costs one quota unit, and being wrong costs her a wrong
// answer spoken with confidence.
func TestTheBroadcastTTLStaysShortEnoughToBeUseful(t *testing.T) {
	if broadcastTTL <= 0 {
		t.Fatal("without a TTL the first answer is kept for the whole session")
	}
	if broadcastTTL > 5*time.Minute {
		t.Errorf("a %v TTL means a changed title is read out wrongly for "+
			"most of a stream", broadcastTTL)
	}
}
