package youtube

import (
	"context"
)

// EndBroadcast takes the live broadcast to "complete".
//
// OBS stopping only stops the pixels: whether the broadcast itself ends is
// YouTube's decision, and it ends one on its own only when auto-stop is turned
// on for it. With auto-stop off, "stop streaming" leaves a broadcast sitting
// live with nothing going into it, which nobody watching can tell apart from
// the stream having broken.
//
// It reports what it did. Nothing to end is not a failure -- she may well have
// ended it in Studio, or never had one -- and saying "there was nothing to
// end" is a different sentence from "it is ended".
func (c *Controller) EndBroadcast(ctx context.Context) (ended bool, err error) {
	// The freshest answer available, because this is the one call where acting
	// on a stale one is expensive: transitioning a broadcast that already
	// finished is an error, and missing one that is still live is the whole
	// bug being fixed.
	broadcast, err := c.ActiveBroadcast(ctx, true)
	if err != nil {
		return false, err
	}
	if broadcast == nil {
		return false, nil
	}

	// Only a broadcast that is actually running can be completed. "upcoming"
	// has never gone live and "complete" is already there; asking YouTube to
	// transition either is an error, and a loud one for something that is not
	// wrong.
	if broadcast.Status != "live" && broadcast.Status != "active" {
		return false, nil
	}

	service, err := c.apiService("liveBroadcasts.transition")
	if err != nil {
		return false, err
	}
	_, err = service.LiveBroadcasts.
		Transition("complete", broadcast.ID, []string{"id", "status"}).
		Context(ctx).Do()
	c.Quota.Spend("liveBroadcasts.transition")
	if err != nil {
		return false, c.classify(err)
	}

	// What is cached now says "live" about something that is not, and the chat
	// id on it belongs to a chat that has stopped.
	c.InvalidateBroadcast()
	return true, nil
}
