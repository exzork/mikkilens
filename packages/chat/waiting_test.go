package chat

import (
	"errors"
	"fmt"
	"testing"

	"github.com/exzork/mikkilens/packages/controllers/youtube"
)

// Why chat has nothing to connect to decides what she hears, and each reason
// has a different fix: going live, or on the right channel; connecting YouTube
// again; or nothing but waiting. Before these had names, every one of them was
// the same silence.
func TestEachReasonForWaitingHasItsOwnStatus(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{&youtube.NoBroadcastError{Reason: "there is no active broadcast"}, "no_broadcast"},
		{&youtube.ExpiredCredentialsError{Reason: "expired"}, "sign_in_expired"},
		{&youtube.NotAuthenticatedError{Reason: "not signed in"}, "signed_out"},
		{fmt.Errorf("looking up the broadcast: %w",
			&youtube.NoBroadcastError{Reason: "none"}), "no_broadcast"},
		{errors.New("dial tcp: i/o timeout"), "waiting"},
	}
	for _, c := range cases {
		if got := waitingFor(c.err); got != c.want {
			t.Errorf("waitingFor(%T) = %q, want %q", c.err, got, c.want)
		}
	}
}
