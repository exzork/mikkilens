package chat

import (
	"testing"
	"time"
)

// The streaming connection is closed by YouTube periodically by design, so
// reconnecting is the normal case rather than the error case. What must never
// happen is reconnecting instantly in a loop: chat would go quiet while
// MikkiLens spun against Google's front end, and neither she nor her viewers
// would be told anything was wrong.

func TestTheFirstRetryWaitsRatherThanReconnectingInstantly(t *testing.T) {
	if got := nextBackoff(0); got != time.Second {
		t.Errorf("the first backoff is %v, want 1s", got)
	}
}

func TestBackoffGrowsButIsCapped(t *testing.T) {
	wait := nextBackoff(0)
	for range 10 {
		previous := wait
		wait = nextBackoff(wait)
		if wait < previous {
			t.Fatalf("backoff shrank from %v to %v", previous, wait)
		}
	}
	if wait != maxStreamBackoff {
		t.Errorf("backoff settled at %v, want the %v cap", wait, maxStreamBackoff)
	}
}

// A stream that ran for a while and then closed is healthy, and waiting after
// it would leave a gap in chat for no reason.
func TestAHealthyStreamIsLongerThanTheRetryCap(t *testing.T) {
	if healthyStream < maxStreamBackoff {
		t.Error("a connection counted as healthy must outlast the retry cap, " +
			"or a failing stream would keep resetting its own backoff")
	}
}
