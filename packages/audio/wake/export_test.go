package wake

import "time"

// WaitIdle blocks until every queued chunk has been scored.
//
// Scoring is asynchronous so it stays off the audio thread, which means a test
// that feeds audio and then reads the score is racing it. Nothing in the
// application needs this; it exists only so the tests can be precise instead
// of sleeping and hoping.
func (d *Detector) WaitIdle(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if d.pending.Load() == 0 {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return false
}
