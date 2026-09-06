// Switching between her channels: one sentence, both halves.
//
// A "channel" here is a pairing, because that is what it is in practice. The
// main channel is an OBS profile holding one stream key plus a YouTube sign-in
// for the account that key belongs to; the music review channel is a different
// profile and a different sign-in. Doing one without the other is the failure
// worth designing against: switching only OBS streams the music review to the
// main channel, and switching only the sign-in reads the wrong chat aloud over
// a broadcast going somewhere else.
//
// It works in both directions. Saying "switch to my music channel" moves both.
// Changing the profile inside OBS -- from the menu, by a Stream Deck key, by
// whoever is helping that day -- moves the sign-in to match, because OBS is the
// thing actually holding the stream key and the application's job is to follow
// what is really on screen rather than what it last told OBS to do.
package engine

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/exzork/mikkilens/packages/audio/feedback"
	"github.com/exzork/mikkilens/packages/controllers/obs"
	"github.com/exzork/mikkilens/packages/controllers/youtube"
	"github.com/exzork/mikkilens/packages/core/config"
	"github.com/exzork/mikkilens/packages/core/fuzzy"
	"github.com/exzork/mikkilens/packages/core/i18n"
	"github.com/exzork/mikkilens/packages/core/intent"
	"github.com/exzork/mikkilens/packages/core/state"
)

// channelMatchThreshold is how close a spoken channel name has to be. Lower
// than the OBS one, because there are two or three channels rather than a dozen
// scenes, so a loose match is far more likely to be the right one than wrong.
const channelMatchThreshold = 60.0

// Channels lists the configured channels.
func (e *Engine) Channels() []config.Channel { return e.Config().YouTube.Channels }

// ActiveChannel is the channel the YouTube sign-in currently belongs to.
func (e *Engine) ActiveChannel() (config.Channel, bool) {
	controller := e.YouTube()
	if controller == nil {
		return config.Channel{}, false
	}
	return e.Config().YouTube.FindChannel(controller.ActiveChannelID())
}

// ResolveChannel finds the channel a spoken name refers to.
//
// Both the name she gave it and the OBS profile name are candidates, so
// whichever of the two she happens to say works. She named them; she should not
// have to remember which of two fields she typed a word into.
func (e *Engine) ResolveChannel(spoken string) (config.Channel, bool) {
	channels := e.Channels()
	if len(channels) == 0 {
		return config.Channel{}, false
	}

	wanted := strings.ToLower(strings.TrimSpace(spoken))
	if wanted == "" {
		return config.Channel{}, false
	}

	candidates := make([]string, 0, len(channels)*2)
	owners := make([]int, 0, len(channels)*2)
	for index, channel := range channels {
		for _, alias := range []string{channel.Name, channel.Profile} {
			if alias == "" {
				continue
			}
			lowered := strings.ToLower(alias)
			if lowered == wanted {
				return channel, true
			}
			candidates = append(candidates, lowered)
			owners = append(owners, index)
		}
	}
	if len(candidates) == 0 {
		return config.Channel{}, false
	}

	index, score := fuzzy.ExtractOne(wanted, candidates, fuzzy.WRatio)
	if index < 0 || score < channelMatchThreshold {
		return config.Channel{}, false
	}
	return channels[owners[index]], true
}

// SwitchChannel moves both halves: the OBS profile that holds the stream key,
// and the YouTube sign-in that reads the chat.
//
// OBS goes first. It is the half that can refuse -- it will not reconfigure an
// output while she is live -- and a refusal there has to leave everything as it
// was rather than half-moved. Swapping the sign-in first and then discovering
// OBS would not follow is the state this application must never be in: chat and
// titles for one channel, stream key for another, and nothing on screen to show
// it.
func (e *Engine) SwitchChannel(ctx context.Context, spoken string) error {
	// One at a time. Two switches overlapping would have OBS on one channel's
	// profile and the sign-in on another's, which is the exact state this is
	// here to prevent.
	e.switching.Lock()
	defer e.switching.Unlock()

	channel, ok := e.ResolveChannel(spoken)
	if !ok {
		e.bus.SayKey("channel.not_found", feedback.Error, i18n.Args{"channel": spoken})
		return nil
	}

	controller := e.YouTube()
	if controller != nil && controller.ActiveChannelID() == channel.ChannelID &&
		channel.ChannelID != "" && e.profileMatches(channel) {
		e.bus.SayKey("channel.already", feedback.Result,
			i18n.Args{"channel": channel.Named()})
		return nil
	}

	// OBS announces a profile change back to us, and that announcement looks
	// exactly like her changing it herself. Saying which one was asked for is
	// what stops the answer being acted on a second time.
	e.expectProfile(channel.Profile)
	if !e.moveOBS(channel) {
		e.expectProfile("")
		return nil
	}
	return e.adoptChannel(ctx, channel, "channel.switched")
}

// expectedProfileGrace is how long an announcement is still taken to be the
// echo of a switch MikkiLens made.
//
// Long enough to cover a scene collection reload, which OBS does after the
// profile change and which holds the event stream up behind it. Short enough
// that a profile she changes herself a moment later is still followed.
const expectedProfileGrace = 90 * time.Second

func (e *Engine) expectProfile(profile string) {
	e.mu.Lock()
	e.expectedProfile, e.expectedAt = profile, time.Now()
	e.mu.Unlock()
}

// wasExpected reports whether this profile change is the echo of one MikkiLens
// asked for, and forgets it either way: the echo comes once.
func (e *Engine) wasExpected(profile string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()

	expected := e.expectedProfile != "" &&
		strings.EqualFold(e.expectedProfile, profile) &&
		time.Since(e.expectedAt) < expectedProfileGrace
	if expected {
		e.expectedProfile = ""
	}
	return expected
}

// profileMatches reports whether OBS is already on this channel's profile.
//
// Checked before saying "already on the music channel", because the sign-in and
// the profile can disagree -- OBS was closed when she last switched, say -- and
// answering "already there" would leave the disagreement in place, which is the
// one thing this must not do.
func (e *Engine) profileMatches(channel config.Channel) bool {
	controller := e.OBS()
	if controller == nil || !controller.Connected() || channel.Profile == "" {
		return false
	}
	current, err := controller.CurrentProfile()
	return err == nil && strings.EqualFold(current, channel.Profile)
}

// moveOBS loads the channel's profile and scenes, saying why if it cannot.
func (e *Engine) moveOBS(channel config.Channel) bool {
	if channel.Profile == "" {
		// Nothing to move: she has a sign-in for this channel but has not said
		// which OBS profile streams to it. Worth saying rather than switching
		// silently, because going live would use whatever key is loaded.
		e.bus.SayKey("channel.no_profile", feedback.Error,
			i18n.Args{"channel": channel.Named()})
		return false
	}
	if !e.requireOBS() {
		return false
	}
	controller := e.OBS()

	if _, err := controller.SwitchProfile(channel.Profile); err != nil {
		e.sayOBSProblem(err, channel)
		return false
	}
	if channel.SceneCollection != "" {
		// Slow: OBS unloads every source and builds the collection's own. The
		// controller holds the socket closed for the duration, and the call
		// only returns once the scenes are really there.
		e.bus.SayKey("channel.loading_scenes", feedback.Result,
			i18n.Args{"channel": channel.Named()})
		if _, err := controller.SwitchSceneCollection(channel.SceneCollection); err != nil {
			e.sayOBSProblem(err, channel)
			return false
		}
		e.refreshScene()
	}
	return true
}

// sayOBSProblem turns an OBS refusal into the sentence that says what to do.
func (e *Engine) sayOBSProblem(err error, channel config.Channel) {
	switch {
	case errors.As(err, new(*obs.StreamingError)):
		// The one refusal with an action attached, and the one OBS itself will
		// not report: obs-websocket answers a profile switch successfully and
		// then declines to make it, so without this she would be told she had
		// changed channel while still streaming to the old one.
		e.bus.SayKey("channel.live", feedback.Error, i18n.Args{"channel": channel.Named()})
	case errors.As(err, new(*obs.ReloadingError)):
		e.bus.SayKey("channel.busy", feedback.Error)
	default:
		e.bus.SayKey("channel.obs_failed", feedback.Error,
			i18n.Args{"reason": err.Error()})
	}
}

// adoptChannel points the YouTube half at a channel and announces it.
//
// Shared by the spoken command and by following a profile she changed in OBS,
// so both end in the same state and say the same kind of thing.
func (e *Engine) adoptChannel(ctx context.Context, channel config.Channel, key string) error {
	if channel.ChannelID == "" {
		// Written into config.toml by hand without connecting it. There is no
		// sign-in to load, and asking for "whichever is sensible" here would
		// quietly put her on some other channel's chat.
		e.bus.SayKey("channel.not_connected", feedback.Error,
			i18n.Args{"channel": channel.Named()})
		return nil
	}

	controller := e.YouTube()
	if controller == nil {
		// YouTube is switched off entirely. OBS has still moved, which is the
		// half that matters for what goes out, so this is worth saying rather
		// than failing.
		e.rememberActiveChannel(channel.ChannelID)
		e.bus.SayKey(key, feedback.Result, i18n.Args{"channel": channel.Named()})
		return nil
	}

	loaded, err := controller.Use(ctx, channel.ChannelID)
	if err != nil || !loaded {
		if err != nil {
			slog.Warn("could not switch the YouTube sign-in",
				"channel", channel.Named(), "error", err)
		}
		var expired *youtube.ExpiredCredentialsError
		if errors.As(err, &expired) {
			e.bus.SayKey("channel.sign_in_expired", feedback.Error,
				i18n.Args{"channel": channel.Named()})
		} else {
			e.bus.SayKey("channel.not_connected", feedback.Error,
				i18n.Args{"channel": channel.Named()})
		}
		e.store.Update(state.Changes{"youtube": state.Disconnected})
		return nil
	}

	e.rememberActiveChannel(channel.ChannelID)

	// Everything learned about the old channel is about the old channel: its
	// broadcast, its title, its chat. Chat is torn down and started again
	// rather than pointed somewhere new, because a live chat connection belongs
	// to one broadcast and there is no such thing as moving it.
	controller.InvalidateBroadcast()
	e.restartChat()

	e.store.Update(state.Changes{"youtube": state.Connected, "channel": channel.Named()})
	e.bus.SayKey(key, feedback.Result, i18n.Args{"channel": channel.Named()})
	return nil
}

// restartChat stops chat reading and starts it again on the current sign-in.
func (e *Engine) restartChat() {
	e.mu.Lock()
	reader, ingest := e.reader, e.ingest
	e.reader, e.ingest = nil, nil
	e.mu.Unlock()

	if reader != nil {
		reader.Stop()
	}
	if ingest != nil {
		ingest.Stop()
	}
	e.store.Update(state.Changes{"chat": state.Unknown})
	e.startChat()
}

// refreshScene re-reads the current scene after the scenes themselves changed.
func (e *Engine) refreshScene() {
	controller := e.OBS()
	if controller == nil {
		return
	}
	if scene, err := controller.CurrentScene(); err == nil {
		e.store.Update(state.Changes{"current_scene": scene})
	}
	if scenes, err := controller.Scenes(); err == nil {
		e.store.Update(state.Changes{"scenes": scenes})
	}
}

// followProfile is the other direction: OBS changed profile without being asked
// to, so the sign-in moves to match.
//
// Quietly when there is nothing to do, because this fires on every profile
// change including the ones MikkiLens made itself, and announcing a switch
// twice is worse than not announcing it once.
func (e *Engine) followProfile(profile string) {
	if e.wasExpected(profile) {
		// MikkiLens asked for this one. The switch it belongs to is either
		// still running or has already said its piece.
		return
	}

	e.switching.Lock()
	defer e.switching.Unlock()

	settings := e.Config()
	channel, ok := settings.YouTube.ChannelForProfile(profile)
	if !ok {
		// A profile with no channel bound to it. Not an error -- she may keep a
		// recording-only profile -- so it is logged and left alone rather than
		// announced as a problem she has not got.
		slog.Info("no channel is bound to this OBS profile", "profile", profile)
		return
	}

	controller := e.YouTube()
	if controller != nil && controller.ActiveChannelID() == channel.ChannelID {
		return // already there; MikkiLens most likely did this itself
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = e.adoptChannel(ctx, channel, "channel.followed")
}

// rememberActiveChannel writes down where she is, so opening MikkiLens again
// puts her back on the same channel rather than on whichever sorts first.
func (e *Engine) rememberActiveChannel(channelID string) {
	e.mu.Lock()
	if e.settings.YouTube.Active == channelID {
		e.mu.Unlock()
		return
	}
	e.settings.YouTube.Active = channelID
	settings := e.settings
	e.mu.Unlock()

	if _, err := settings.Save(""); err != nil {
		slog.Warn("could not remember the active channel", "error", err)
	}
}

// DisconnectChannel signs one channel out and takes it off the list.
//
// The Disconnect in the YouTube box is every channel at once: the right answer
// to "I am done with this machine", and the wrong one to "this is the channel I
// no longer stream to" -- which on a machine with two would sign her out of the
// one she still uses.
//
// The channel goes from both of the places it lives: the sign-in on disk, and
// the pairing in config that says which OBS profile streams to it. Leaving the
// pairing behind would leave a row that reads as a channel waiting to be
// reconnected, which is exactly what this one is not. A sign-in with no pairing
// at all is removed just the same -- that is a channel she connected and never
// bound to a profile, and it is the one most likely to be here by mistake.
//
// Removing the last one lands on the same state as the Disconnect button,
// including the old single-channel sign-in from before there were several.
func (e *Engine) DisconnectChannel(channelID string) error {
	channelID = strings.TrimSpace(channelID)
	if channelID == "" {
		return errors.New("no channel to disconnect")
	}

	spoken := e.channelSpokenName(channelID)

	// If the open connection is that channel's, it has to be let go before the
	// token is: signing out is what stops the chat reader and the polling that
	// belong to it.
	if controller := e.YouTube(); controller != nil && controller.ActiveChannelID() == channelID {
		controller.SignOut()
		e.OnYouTubeDisconnected()

		e.mu.Lock()
		e.youtube = nil
		e.mu.Unlock()
	}
	youtube.ForgetAccount(channelID)

	e.mu.Lock()
	kept := make([]config.Channel, 0, len(e.settings.YouTube.Channels))
	for _, channel := range e.settings.YouTube.Channels {
		if channel.ChannelID != channelID {
			kept = append(kept, channel)
		}
	}
	e.settings.YouTube.Channels = kept
	if e.settings.YouTube.Active == channelID {
		e.settings.YouTube.Active = ""
	}
	settings := e.settings
	e.mu.Unlock()

	if _, err := settings.Save(""); err != nil {
		return err
	}

	if len(kept) == 0 && !youtube.HasAccounts() {
		// Nothing left to be signed in as. Cleared right down, so that what is
		// on disk agrees with what the settings page now says.
		youtube.ForgetAllAccounts()
		if err := e.setYouTubeEnabled(false); err != nil {
			return err
		}
	}

	e.bus.SayKey("channel.disconnected", feedback.Result, i18n.Args{"channel": spoken})
	return nil
}

// channelSpokenName is what to call a channel out loud: the name she gave it,
// then what YouTube calls it, then the profile, and the id only if it has
// nothing else -- which is the order of how much of it she wrote herself.
func (e *Engine) channelSpokenName(channelID string) string {
	if channel, ok := e.Config().YouTube.FindChannel(channelID); ok {
		for _, candidate := range []string{channel.Name, channel.Profile} {
			if strings.TrimSpace(candidate) != "" {
				return candidate
			}
		}
	}
	if account, ok := youtube.LoadAccount(channelID); ok && account.ChannelTitle != "" {
		return account.ChannelTitle
	}
	return channelID
}

// RegisterChannel records a channel she has just connected.
//
// The sign-in knows the channel id and its name; what it cannot know is which
// OBS profile streams to it. The profile loaded right now is the best guess
// there is and usually the right one -- connecting a channel is something done
// while set up for it -- but it is only taken when no other channel has claimed
// that profile already, because guessing wrong there would quietly point two
// channels at one stream key.
func (e *Engine) RegisterChannel(account youtube.Account) {
	if account.ChannelID == "" {
		return
	}

	e.mu.Lock()
	settings := e.settings
	e.mu.Unlock()

	for index, existing := range settings.YouTube.Channels {
		if existing.ChannelID != account.ChannelID {
			continue
		}
		// Known already. Refresh the name from YouTube only if she has not
		// given it one of her own.
		if existing.Name == "" && account.ChannelTitle != "" {
			e.mu.Lock()
			e.settings.YouTube.Channels[index].Name = account.ChannelTitle
			settings = e.settings
			e.mu.Unlock()
			if _, err := settings.Save(""); err != nil {
				slog.Warn("could not save the channel name", "error", err)
			}
		}
		return
	}

	added := config.Channel{Name: account.ChannelTitle, ChannelID: account.ChannelID}
	if profile := e.unclaimedProfile(settings); profile != "" {
		added.Profile = profile
	}

	e.mu.Lock()
	e.settings.YouTube.Channels = append(e.settings.YouTube.Channels, added)
	e.settings.YouTube.Active = account.ChannelID
	settings = e.settings
	e.mu.Unlock()

	if _, err := settings.Save(""); err != nil {
		slog.Warn("could not save the new channel", "error", err)
	}
}

// unclaimedProfile is the OBS profile loaded now, if no channel already claims
// it. Empty when OBS is not there or the profile is spoken for.
func (e *Engine) unclaimedProfile(settings config.Config) string {
	controller := e.OBS()
	if controller == nil || !controller.Connected() {
		return ""
	}
	current, err := controller.CurrentProfile()
	if err != nil || current == "" {
		return ""
	}
	if _, taken := settings.YouTube.ChannelForProfile(current); taken {
		return ""
	}
	return current
}

// -- commands -----------------------------------------------------------------

func channelHandlers(e *Engine) map[string]intent.Handler {
	return map[string]intent.Handler{
		"switch_channel":  e.switchChannel,
		"current_channel": e.currentChannel,
		"list_channels":   e.listChannels,
	}
}

// switchChannelTimeout is generous because a scene collection reload is inside
// it: OBS tearing down every source and building another set is seconds of
// work, and giving up halfway would leave the two halves disagreeing.
const switchChannelTimeout = 2 * time.Minute

func (e *Engine) switchChannel(slots map[string]string) error {
	ctx, cancel := context.WithTimeout(context.Background(), switchChannelTimeout)
	defer cancel()
	return e.SwitchChannel(ctx, strings.TrimSpace(slots["channel"]))
}

func (e *Engine) currentChannel(map[string]string) error {
	channel, ok := e.ActiveChannel()
	if !ok {
		// Signed in to something the config does not name. Saying the channel's
		// own title is more use than saying nothing, and it is the name she
		// would have to add to config.toml anyway.
		if controller := e.YouTube(); controller != nil && controller.Authenticated() {
			e.bus.SayKey("channel.current", feedback.Result,
				i18n.Args{"channel": controller.ActiveAccount().Named()})
			return nil
		}
		e.bus.SayKey("channel.none", feedback.Error)
		return nil
	}
	e.bus.SayKey("channel.current", feedback.Result, i18n.Args{"channel": channel.Named()})
	return nil
}

func (e *Engine) listChannels(map[string]string) error {
	channels := e.Channels()
	if len(channels) == 0 {
		e.bus.SayKey("channel.none", feedback.Error)
		return nil
	}

	names := make([]string, 0, len(channels))
	for _, channel := range channels {
		names = append(names, channel.Named())
	}
	e.bus.SayKey("channel.list", feedback.Result,
		i18n.Args{"channels": strings.Join(names, ", ")})
	return nil
}
