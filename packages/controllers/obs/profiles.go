// Profiles and scene collections: the two things OBS keeps one of per channel.
//
// A *profile* holds the stream settings -- the service, the server and the
// stream key -- so one profile per channel is how OBS itself expects somebody
// broadcasting to two channels to work. MikkiLens switches the profile and
// never reads the key: the credential for going live stays inside OBS, where
// she already trusts it, and nothing here can log it or send it anywhere.
//
// A *scene collection* holds the scenes and sources. Changing one makes OBS
// unload everything and build it again, which is slow and, while it runs, a
// window in which obs-websocket documents any request as undefined behaviour
// that can crash OBS. So the collection switch is fenced: requests are refused
// from the "changing" event until the "changed" one, and the caller waits for
// the collection to actually be there before being told it worked.
package obs

import (
	"fmt"
	"time"

	obsconfig "github.com/andreykaipov/goobs/api/requests/config"
)

// collectionSwitchTimeout is how long a scene collection may take to load
// before the wait gives up and says so.
//
// The request itself blocks in OBS until the load finishes, so this is only
// reached when something has genuinely gone wrong -- but it has to exist,
// because the alternative is a voice command that never answers.
const collectionSwitchTimeout = 60 * time.Second

// Profiles lists every OBS profile, and which one is loaded now.
func (c *Controller) Profiles() (current string, all []string, err error) {
	client, err := c.request()
	if err != nil {
		return "", nil, err
	}
	response, err := client.Config.GetProfileList()
	if err != nil {
		return "", nil, c.fail(err)
	}
	return response.CurrentProfileName, response.Profiles, nil
}

// CurrentProfile is the profile OBS has loaded, which is to say the channel its
// stream key points at.
func (c *Controller) CurrentProfile() (string, error) {
	current, _, err := c.Profiles()
	return current, err
}

// SwitchProfile loads the profile whose name best matches, and returns the real
// name so it can be read back to her.
//
// It refuses while she is live, and that refusal is the whole point of the
// method rather than a nicety. obs-websocket does not check: SetCurrentProfile
// validates only that the name exists and then queues the change, so asking for
// it mid-stream comes back *successful* while OBS quietly declines to swap the
// output settings out from under a running broadcast. A command that reports
// success and changes nothing is the exact failure this application exists to
// prevent, so the check is made here where it can be said out loud.
func (c *Controller) SwitchProfile(spoken string) (string, error) {
	current, names, err := c.Profiles()
	if err != nil {
		return "", err
	}
	actual, ok := bestName(spoken, names)
	if !ok {
		return "", &Error{Reason: fmt.Sprintf("no OBS profile matching %q", spoken)}
	}
	if actual == current {
		return actual, nil
	}
	if live, err := c.IsStreaming(); err == nil && live {
		return "", &StreamingError{Reason: "OBS will not change profile while you are live"}
	}

	client, err := c.request()
	if err != nil {
		return "", err
	}
	if _, err := client.Config.SetCurrentProfile(
		obsconfig.NewSetCurrentProfileParams().WithProfileName(actual)); err != nil {
		return "", c.fail(err)
	}

	// Read it back rather than trusting the request. Because obs-websocket
	// answers before OBS has acted -- and answers the same way when OBS declines
	// to act at all -- this is the only thing that distinguishes a switch that
	// happened from one that did not.
	if err := c.waitForProfile(actual); err != nil {
		return "", err
	}
	return actual, nil
}

// waitForProfile blocks until OBS reports the profile loaded.
func (c *Controller) waitForProfile(want string) error {
	deadline := time.Now().Add(10 * time.Second)
	for {
		current, err := c.CurrentProfile()
		if err == nil && current == want {
			return nil
		}
		if time.Now().After(deadline) {
			return &Error{Reason: fmt.Sprintf(
				"OBS did not switch to the %s profile", want)}
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// SceneCollections lists every scene collection, and which one is loaded.
func (c *Controller) SceneCollections() (current string, all []string, err error) {
	client, err := c.request()
	if err != nil {
		return "", nil, err
	}
	response, err := client.Config.GetSceneCollectionList()
	if err != nil {
		return "", nil, c.fail(err)
	}
	return response.CurrentSceneCollectionName, response.SceneCollections, nil
}

// CurrentSceneCollection is the set of scenes OBS has loaded.
func (c *Controller) CurrentSceneCollection() (string, error) {
	current, _, err := c.SceneCollections()
	return current, err
}

// SwitchSceneCollection loads the scene collection whose name best matches.
//
// This is the slow one. OBS destroys every source and builds the next
// collection's, the request blocks for as long as that takes, and requests made
// during it are undefined behaviour -- so the reload fence set by the event
// stream is what keeps everything else off the socket meanwhile, and the answer
// only comes once the collection is really loaded.
func (c *Controller) SwitchSceneCollection(spoken string) (string, error) {
	current, names, err := c.SceneCollections()
	if err != nil {
		return "", err
	}
	actual, ok := bestName(spoken, names)
	if !ok {
		return "", &Error{Reason: fmt.Sprintf("no OBS scene collection matching %q", spoken)}
	}
	if actual == current {
		return actual, nil
	}
	if live, err := c.IsStreaming(); err == nil && live {
		return "", &StreamingError{
			Reason: "OBS will not change scene collection while you are live"}
	}

	client, err := c.request()
	if err != nil {
		return "", err
	}

	// The fence is raised here rather than waiting for the "changing" event,
	// because the event and this goroutine race: OBS can begin unloading before
	// the event reaches us, and anything that slipped onto the socket in that
	// gap is exactly what the warning is about.
	c.setReloading(true)
	_, err = client.Config.SetCurrentSceneCollection(
		obsconfig.NewSetCurrentSceneCollectionParams().WithSceneCollectionName(actual))
	if err != nil {
		c.setReloading(false)
		return "", c.fail(err)
	}

	if err := c.waitForCollection(actual); err != nil {
		return "", err
	}
	return actual, nil
}

// waitForCollection blocks until the reload fence lifts and OBS reports the
// collection loaded.
func (c *Controller) waitForCollection(want string) error {
	deadline := time.Now().Add(collectionSwitchTimeout)
	for {
		if !c.Reloading() {
			if current, err := c.CurrentSceneCollection(); err == nil && current == want {
				return nil
			}
		}
		if time.Now().After(deadline) {
			// Whatever OBS is doing, it is not answering, and leaving the fence
			// up would take every other command down with it.
			c.setReloading(false)
			return &Error{Reason: fmt.Sprintf(
				"OBS did not finish loading the %s scenes", want)}
		}
		time.Sleep(500 * time.Millisecond)
	}
}
