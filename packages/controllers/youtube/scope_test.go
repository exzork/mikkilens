package youtube

import (
	"testing"

	yt "google.golang.org/api/youtube/v3"
)

// What Google reads out on the consent screen is decided entirely by the scope
// asked for, and that sentence is the last thing standing between her and
// giving up on setup. It is worth a test.

// acceptedByEveryCall is what the YouTube Data API discovery document lists as
// sufficient for every method MikkiLens calls. The binding constraint is
// liveBroadcasts.update, the one write: it accepts only youtube and
// youtube.force-ssl. Everything else would also accept youtube.readonly.
var acceptedByEveryCall = map[string]bool{
	yt.YoutubeScope:         true,
	yt.YoutubeForceSslScope: true,
}

func TestTheScopeIsEnoughForEveryCallMade(t *testing.T) {
	if !acceptedByEveryCall[Scope] {
		t.Fatalf("scope %q cannot change the broadcast title, so setting it by "+
			"voice would fail at the API rather than at the settings page", Scope)
	}
}

// force-ssl is described to her as "see, edit, and permanently delete your
// YouTube videos, ratings, comments, and captions". MikkiLens deletes nothing
// and never touches a comment or a caption, so asking for that would be asking
// for trust it does not need -- and she is being read that sentence aloud by
// someone else.
func TestTheScopeDoesNotAskForDeletionRights(t *testing.T) {
	if Scope == yt.YoutubeForceSslScope {
		t.Error("force-ssl asks to permanently delete her videos; " +
			"plain youtube covers everything MikkiLens actually does")
	}
}

// If this ever fails, something added a call needing a wider scope. Widening
// it is a decision about what she is asked to agree to, not an implementation
// detail, so it should not happen quietly.
func TestTheScopeIsStillTheNarrowOne(t *testing.T) {
	if Scope != yt.YoutubeScope {
		t.Errorf("scope is %q, want %q; widening it changes the consent screen",
			Scope, yt.YoutubeScope)
	}
}
