package engine

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/exzork/mikkilens/packages/audio/feedback"
	"github.com/exzork/mikkilens/packages/chat"
	"github.com/exzork/mikkilens/packages/controllers/llm"
	"github.com/exzork/mikkilens/packages/controllers/vision"
	"github.com/exzork/mikkilens/packages/controllers/youtube"
	"github.com/exzork/mikkilens/packages/core/i18n"
	"github.com/exzork/mikkilens/packages/core/intent"
	"github.com/exzork/mikkilens/packages/core/state"
)

// This file maps command ids from commands.toml to the controllers.
//
// It is kept apart from both the router (which knows nothing about OBS) and
// the controllers (which know nothing about speech), so the mapping from "what
// she said" to "what happens" is readable in one place.
//
// Every handler ends by speaking. A handler that returns quietly is a bug.

// -- OBS ----------------------------------------------------------------------

func obsHandlers(e *Engine) map[string]intent.Handler {
	return map[string]intent.Handler{
		"go_live":     e.goLive,
		"stop_stream": e.stopStream,
		"is_live":     e.isLive,
		"mute_mic":    func(map[string]string) error { return e.setMicMuted(true) },
		"unmute_mic":  func(map[string]string) error { return e.setMicMuted(false) },
		"mic_status":  e.micStatus,
		"status":      e.status,
	}
}

// requireOBS speaks the reason OBS is unavailable rather than returning a bare
// error, because "OBS is not responding" is what she needs to hear.
func (e *Engine) requireOBS() bool {
	controller := e.OBS()
	if controller == nil || !controller.Connected() {
		e.bus.SayKey("obs.not_responding", feedback.Error)
		return false
	}
	return true
}

func (e *Engine) goLive(map[string]string) error {
	if !e.requireOBS() {
		return nil
	}
	controller := e.OBS()

	live, err := controller.IsStreaming()
	if err != nil {
		return err
	}
	if live {
		e.bus.SayKey("obs.already_live", feedback.Result)
		return nil
	}
	if err := controller.StartStream(); err != nil {
		return err
	}
	e.store.Update(state.Changes{"streaming": true})
	e.bus.SayKey("obs.stream_started", feedback.Result)
	return nil
}

func (e *Engine) stopStream(map[string]string) error {
	if !e.requireOBS() {
		return nil
	}
	controller := e.OBS()

	live, err := controller.IsStreaming()
	if err != nil {
		return err
	}
	if !live {
		e.bus.SayKey("obs.not_streaming", feedback.Result)
		return nil
	}
	if err := controller.StopStream(); err != nil {
		return err
	}
	e.store.Update(state.Changes{"streaming": false})
	e.bus.SayKey("obs.stream_stopped", feedback.Result)

	// On the same answer, not a second question. Ending the broadcast is what
	// "stop the stream" means to everyone watching it, and asking twice about
	// one intention is a worse trade than folding it into one prompt that
	// says what it will do.
	e.endBroadcast()
	return nil
}

// endBroadcast finishes the YouTube broadcast that OBS was feeding.
//
// Stopping OBS stops the pixels and nothing else. YouTube ends the broadcast
// on its own only when auto-stop is turned on for it, and with that off the
// broadcast sits there live with nothing going into it -- which to everyone
// watching looks like the stream having broken rather than having finished.
//
// This runs off the back of the one confirmation that stopping the stream
// already asks for, which is why that question now says the broadcast ends
// too: the answer covers something irreversible, so it has to name it.
//
// Nothing is said when there was nothing to end. She has already been told
// the stream stopped, and "there was no broadcast to end" on top of it would
// be a sentence about YouTube every single time she stops a stream without a
// broadcast running.
func (e *Engine) endBroadcast() {
	controller := e.YouTube()
	if controller == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	ended, err := controller.EndBroadcast(ctx)
	switch {
	case err != nil:
		// Not signed in is the ordinary case, not a fault: without a sign-in
		// there is no broadcast to end and never was. It costs no quota
		// either, because the lookup fails before the call.
		var unauthenticated *youtube.NotAuthenticatedError
		if errors.As(err, &unauthenticated) {
			return
		}
		slog.Error("could not end the broadcast", "error", err)
		e.bus.SayKey("youtube.broadcast_end_failed", feedback.Error)
	case ended:
		e.bus.SayKey("youtube.broadcast_ended", feedback.Result)
	}
}

func (e *Engine) isLive(map[string]string) error {
	if !e.requireOBS() {
		return nil
	}
	live, err := e.OBS().IsStreaming()
	if err != nil {
		return err
	}
	e.store.Update(state.Changes{"streaming": live})
	e.bus.SayKey(liveKey(live), feedback.Result)
	return nil
}

func liveKey(live bool) string {
	if live {
		return "status.live"
	}
	return "status.not_live"
}

// Scenes and sources are not commands.
//
// They were, and taking them out is not a loss of function: scenes are on the
// Stream Deck, one key each, and a key that switches a scene is faster and
// more certain than a sentence about it -- especially a sentence containing a
// scene name, which is exactly the kind of proper noun recognition is worst
// at. What is left here is what a key cannot do as well: the things that need
// a question asked first, the things that need an answer read back, and the
// microphone.
//
// The scene still reaches her: "status" says which one is up, along with
// everything else she cannot see. And switching a channel still moves the OBS
// profile, because that is one thing meaning two, and neither half is any use
// without the other.

func (e *Engine) setMicMuted(muted bool) error {
	if !e.requireOBS() {
		return nil
	}
	if err := e.OBS().SetMicMuted(muted); err != nil {
		return err
	}
	e.store.Update(state.Changes{"mic_muted": muted})
	if muted {
		e.bus.SayKey("obs.mic_muted", feedback.Result)
	} else {
		e.bus.SayKey("obs.mic_unmuted", feedback.Result)
	}
	return nil
}

func (e *Engine) micStatus(map[string]string) error {
	if !e.requireOBS() {
		return nil
	}
	muted, err := e.OBS().MicMuted()
	if err != nil {
		return err
	}
	e.store.Update(state.Changes{"mic_muted": muted})
	if muted {
		e.bus.SayKey("status.mic_muted", feedback.Result)
	} else {
		e.bus.SayKey("status.mic_live", feedback.Result)
	}
	return nil
}

// status makes one pass over everything, so she can orient herself without
// asking five separate questions.
func (e *Engine) status(map[string]string) error {
	locale := e.Locale()
	snapshot := e.store.Snapshot()
	parts := []string{}

	controller := e.OBS()
	if controller != nil && controller.Connected() {
		live, liveErr := controller.IsStreaming()
		scene, sceneErr := controller.CurrentScene()
		muted, muteErr := controller.MicMuted()

		if liveErr == nil && sceneErr == nil && muteErr == nil {
			e.store.Update(state.Changes{
				"streaming": live, "current_scene": scene, "mic_muted": muted,
			})
			parts = append(parts, locale.T(liveKey(live)))
			parts = append(parts, locale.T("status.scene", i18n.Args{"scene": scene}))
			if muted {
				parts = append(parts, locale.T("status.mic_muted"))
			} else {
				parts = append(parts, locale.T("status.mic_live"))
			}
		} else {
			parts = append(parts, locale.T("obs.not_responding"))
		}
	} else {
		parts = append(parts, locale.T("obs.not_responding"))
	}

	if snapshot["youtube"] == state.Connected {
		parts = append(parts, locale.T("status.viewers",
			i18n.Args{"count": snapshot["viewer_count"]}))
	}
	if snapshot["chat_reading"] != state.ChatStopped {
		backlog, _ := snapshot["chat_backlog"].(int)
		if backlog > 0 {
			parts = append(parts, locale.T("status.chat_backlog", i18n.Args{"count": backlog}))
		} else {
			parts = append(parts, locale.T("status.chat_empty"))
		}
		// Said last, and only when it is on. A muted chat with a backlog
		// behind it sounds exactly like a quiet one, and this is the sentence
		// that tells the two apart.
		if e.bus.ChatMuted() {
			parts = append(parts, locale.T("status.chat_muted"))
		}
	}

	e.bus.Say(strings.Join(parts, " "), feedback.Result)
	return nil
}

// -- YouTube ------------------------------------------------------------------

func youTubeHandlers(e *Engine) map[string]intent.Handler {
	return map[string]intent.Handler{
		"get_title":    e.getTitle,
		"set_title":    e.setTitle,
		"viewer_count": e.viewerCount,
	}
}

// requireYouTube says "YouTube is not connected" rather than reporting the
// command as missing, which is the difference between a fixable problem and a
// mysterious one.
//
// Reading only needs an API key, so this asks whether anything is available
// rather than whether she is signed in. Refusing to say the viewer count while
// holding a working key would be the wrong answer to the right question.
func (e *Engine) requireYouTube() bool {
	controller := e.YouTube()
	if controller == nil || !controller.Available() {
		e.bus.SayKey("youtube.not_connected", feedback.Error)
		return false
	}
	return true
}

func (e *Engine) getTitle(map[string]string) error {
	if !e.requireYouTube() {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	title, err := e.YouTube().Title(ctx)
	if err != nil {
		e.bus.SayKey("youtube.no_broadcast", feedback.Error)
		return nil
	}
	e.store.Update(state.Changes{"broadcast_title": title})
	e.bus.SayKey("youtube.title_is", feedback.Result, i18n.Args{"text": title})
	return nil
}

func (e *Engine) setTitle(slots map[string]string) error {
	if !e.requireYouTube() {
		return nil
	}
	text := strings.TrimSpace(slots["text"])
	if text == "" {
		e.bus.SayKey("listen.no_speech", feedback.Result)
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if err := e.YouTube().SetTitle(ctx, text); err != nil {
		// Writing is the one thing a key cannot do. Saying "there is no
		// broadcast" here would send her looking for a stream that is running
		// perfectly well.
		var expired *youtube.ExpiredCredentialsError
		switch {
		case errors.As(err, &expired):
			// A sign-in that used to work and has stopped needs a different
			// answer from one that was never made: this one is fixed in OBS,
			// and nothing else she tries will fix it.
			e.bus.SayKey("youtube.sign_in_expired", feedback.Error)
		case errors.As(err, new(*youtube.NotAuthenticatedError)):
			e.bus.SayKey("youtube.needs_sign_in", feedback.Error)
		default:
			e.bus.SayKey("youtube.no_broadcast", feedback.Error)
		}
		return nil
	}
	e.store.Update(state.Changes{"broadcast_title": text})
	e.bus.SayKey("youtube.title_changed", feedback.Result, i18n.Args{"text": text})
	return nil
}

func (e *Engine) viewerCount(map[string]string) error {
	if !e.requireYouTube() {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	count, err := e.YouTube().ViewerCount(ctx)
	if err != nil {
		e.bus.SayKey("youtube.no_broadcast", feedback.Error)
		return nil
	}
	e.store.Update(state.Changes{"viewer_count": count})
	e.bus.SayKey("status.viewers", feedback.Result, i18n.Args{"count": count})
	return nil
}

// -- chat ---------------------------------------------------------------------

func chatHandlers(e *Engine) map[string]intent.Handler {
	return map[string]intent.Handler{
		"chat_pause":       func(map[string]string) error { return e.withReader((*chat.Reader).Pause) },
		"chat_resume":      func(map[string]string) error { return e.withReader((*chat.Reader).Resume) },
		"chat_skip_to_now": func(map[string]string) error { return e.withReaderInt((*chat.Reader).SkipToNow) },
		"chat_behind":      func(map[string]string) error { return e.withReaderInt((*chat.Reader).ReportBacklog) },
		"chat_summarize":   e.summarizeChat,
		"chat_mute":        func(map[string]string) error { e.SetChatMute(true); return nil },
		"chat_unmute":      func(map[string]string) error { e.SetChatMute(false); return nil },
	}
}

// requireReader speaks the reason chat is unavailable, rather than reporting
// the command as missing.
func (e *Engine) requireReader() *chat.Reader {
	reader := e.ChatReader()
	if reader == nil {
		e.bus.SayKey("youtube.not_connected", feedback.Error)
		return nil
	}
	return reader
}

func (e *Engine) withReader(action func(*chat.Reader) bool) error {
	if reader := e.requireReader(); reader != nil {
		action(reader)
	}
	return nil
}

func (e *Engine) withReaderInt(action func(*chat.Reader) int) error {
	if reader := e.requireReader(); reader != nil {
		action(reader)
	}
	return nil
}

func (e *Engine) summarizeChat(map[string]string) error {
	reader := e.requireReader()
	if reader == nil {
		return nil
	}
	pending := reader.PendingMessages()
	if len(pending) == 0 {
		e.bus.SayKey("chat.up_to_date", feedback.Result)
		return nil
	}

	// Announced before the round trip, not after: several seconds of silence
	// reads as "it ignored me".
	e.bus.SayKey("chat.summarizing", feedback.Result)

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()

		transcript := make([][2]string, 0, len(pending))
		for _, message := range pending {
			transcript = append(transcript, [2]string{message.Author, message.Text})
		}

		summary, err := llm.New(e.Config(), e.Locale()).SummarizeChat(ctx, transcript)
		if err != nil {
			e.bus.SayKey("error.generic", feedback.Error, i18n.Args{"reason": err.Error()})
			return
		}
		e.bus.SayKey("chat.summary", feedback.Result, i18n.Args{"text": summary})
		// Summarising is a way of catching up, so the backlog is consumed.
		reader.SkipToNow()
	}()
	return nil
}

// -- vision -------------------------------------------------------------------

func visionHandlers(e *Engine) map[string]intent.Handler {
	return map[string]intent.Handler{"ask_screen": e.askScreen}
}

func (e *Engine) askScreen(slots map[string]string) error {
	settings := e.Config()
	if !settings.Model.Configured() {
		e.bus.SayKey("vision.no_provider", feedback.Error)
		return nil
	}
	question := strings.TrimSpace(slots["question"])

	// Announced before the round trip: two to four seconds of silence reads as
	// having been ignored.
	e.bus.SayKey("vision.looking", feedback.Result)

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()

		answer, err := vision.New(settings, e.Locale()).Describe(ctx, question)
		if err != nil {
			e.store.Update(state.Changes{"vision": state.Errored})
			e.bus.SayKey("vision.failed", feedback.Error, i18n.Args{"reason": err.Error()})
			return
		}
		e.store.Update(state.Changes{"vision": state.Connected})
		e.bus.Say(answer, feedback.Result)
	}()
	return nil
}
