// The settings page's half of running more than one channel.
//
// Everything here is also voice-operable, which is the point of the
// application -- but the pairing itself is not something voice can set up.
// Naming a channel is fine to say out loud; telling MikkiLens that YouTube
// channel UC-something-long belongs to the OBS profile called "Music" is not.
// So the binding is made once, on this page, by whoever is helping, and after
// that switching is a sentence she says.
package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/exzork/mikkilens/packages/controllers/youtube"
	"github.com/exzork/mikkilens/packages/core/config"
)

func (s *Server) channelRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/youtube/channels", s.handleChannels)
	mux.HandleFunc("/api/youtube/connect-channel", only(http.MethodPost, s.connectChannel))
	mux.HandleFunc("/api/youtube/switch", only(http.MethodPost, s.switchChannel))
	mux.HandleFunc("/api/obs/profiles", only(http.MethodGet, s.obsProfiles))
}

func (s *Server) handleChannels(writer http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodGet:
		s.listChannels(writer, request)
	case http.MethodPut:
		s.saveChannels(writer, request)
	default:
		fail(writer, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// channelView is one channel as the settings page needs it: the pairing from
// config, plus whether the sign-in behind it is actually there.
//
// Connected is worth its own field rather than being inferred from the channel
// existing in config, because the two come apart in the way that matters most:
// a Google Cloud project still in Testing expires refresh tokens after seven
// days, so a channel she set up last week is configured, named, bound to a
// profile -- and cannot be streamed to until somebody presses Connect.
type channelView struct {
	Name            string `json:"name"`
	ChannelID       string `json:"channel_id"`
	Profile         string `json:"obs_profile"`
	SceneCollection string `json:"obs_scene_collection"`
	Title           string `json:"channel_title"`
	Connected       bool   `json:"connected"`
	Active          bool   `json:"active"`
}

func (s *Server) listChannels(writer http.ResponseWriter, _ *http.Request) {
	settings := s.engine.Config()

	signedIn := map[string]youtube.Account{}
	for _, account := range youtube.Accounts() {
		signedIn[account.ChannelID] = account
	}

	active := ""
	if controller := s.engine.YouTube(); controller != nil {
		active = controller.ActiveChannelID()
	}

	views := make([]channelView, 0, len(settings.YouTube.Channels))
	for _, channel := range settings.YouTube.Channels {
		account, connected := signedIn[channel.ChannelID]
		views = append(views, channelView{
			Name:            channel.Name,
			ChannelID:       channel.ChannelID,
			Profile:         channel.Profile,
			SceneCollection: channel.SceneCollection,
			Title:           account.ChannelTitle,
			Connected:       connected,
			Active:          channel.ChannelID != "" && channel.ChannelID == active,
		})
	}

	// A sign-in with no entry in config is a channel she connected but never
	// bound to a profile. It has to be listed, or it is invisible on the one
	// page where the binding can be made.
	for _, account := range youtube.Accounts() {
		if _, known := settings.YouTube.FindChannel(account.ChannelID); known {
			continue
		}
		views = append(views, channelView{
			Name:      account.ChannelTitle,
			ChannelID: account.ChannelID,
			Title:     account.ChannelTitle,
			Connected: true,
			Active:    account.ChannelID == active,
		})
	}

	respond(writer, http.StatusOK, map[string]any{"channels": views})
}

// saveChannels writes the pairings back.
//
// A channel with no id is dropped rather than saved. The id is what names the
// sign-in file and what the switch looks up, so an entry without one is a row
// that can never work -- and keeping it would put a channel in her spoken list
// that answers "not connected" every time she asks for it.
func (s *Server) saveChannels(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Channels []config.Channel `json:"channels"`
	}
	if !decode(writer, request, &body) {
		return
	}

	kept := make([]config.Channel, 0, len(body.Channels))
	for _, channel := range body.Channels {
		if channel.ChannelID == "" {
			continue
		}
		kept = append(kept, channel)
	}

	settings := s.engine.Config()
	settings.YouTube.Channels = kept
	if err := s.engine.ApplyConfig(settings); err != nil {
		fail(writer, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := settings.Save(""); err != nil {
		fail(writer, http.StatusInternalServerError, err.Error())
		return
	}
	respond(writer, http.StatusOK, map[string]any{"ok": true, "count": len(kept)})
}

// connectChannel runs the consent flow for another channel.
//
// Same shape as /api/youtube/connect, and separate for the same reason the
// engine keeps them apart: that one means "I have no sign-in", this one means
// "I have one and want another", and only one of them should be reachable by
// pressing the same button twice.
func (s *Server) connectChannel(writer http.ResponseWriter, request *http.Request) {
	if err := s.engine.ConnectChannel(request.Context()); err != nil {
		fail(writer, http.StatusBadGateway, err.Error())
		return
	}
	respond(writer, http.StatusOK, map[string]any{"ok": true})
}

// switchChannelTimeout has to cover an OBS scene collection reload, which is
// seconds of OBS destroying and rebuilding every source.
const switchChannelTimeout = 2 * time.Minute

func (s *Server) switchChannel(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Channel string `json:"channel"`
	}
	if !decode(writer, request, &body) {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), switchChannelTimeout)
	defer cancel()

	// The engine says what happened out loud, including the refusals -- she is
	// in the room, and the page is for whoever is helping. So this reports that
	// the request was made, and the state feed carries the result.
	if err := s.engine.SwitchChannel(ctx, body.Channel); err != nil {
		fail(writer, http.StatusBadGateway, err.Error())
		return
	}
	respond(writer, http.StatusOK, map[string]any{"ok": true})
}

// obsProfiles lists what OBS has, so the pairing can be picked from a list
// rather than typed. A profile name typed with the wrong capitalisation is a
// switch that silently does nothing, and this is the fix for that.
func (s *Server) obsProfiles(writer http.ResponseWriter, _ *http.Request) {
	controller := s.engine.OBS()
	if controller == nil || !controller.Connected() {
		respond(writer, http.StatusOK, map[string]any{
			"connected": false, "profiles": []string{}, "scene_collections": []string{},
		})
		return
	}

	result := map[string]any{"connected": true}
	if current, all, err := controller.Profiles(); err == nil {
		result["current_profile"], result["profiles"] = current, all
	} else {
		result["profiles"] = []string{}
		result["error"] = err.Error()
	}
	if current, all, err := controller.SceneCollections(); err == nil {
		result["current_scene_collection"], result["scene_collections"] = current, all
	} else {
		result["scene_collections"] = []string{}
	}
	respond(writer, http.StatusOK, result)
}
