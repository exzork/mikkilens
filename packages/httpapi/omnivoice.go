package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"time"

	"github.com/exzork/mikkilens/packages/audio/capture"
	"github.com/exzork/mikkilens/packages/audio/feedback"
	"github.com/exzork/mikkilens/packages/audio/tts/omnivoice"
	"github.com/exzork/mikkilens/packages/core/i18n"
)

// Cloning a voice, over the API the settings page already talks to.
//
// OmniVoice takes its voice from a recording, and until now the way to give it
// one was to put a wav file in a folder. This is the rest of it: record,
// choose a file, list, remove -- none of which needs a file manager open.
//
// Everything here says what it is doing out loud as well as returning it,
// because that is what the rest of the application does and because this is a
// job with a pause in the middle. Encoding a recording takes a few seconds,
// and a few seconds of nothing is indistinguishable from something having gone
// wrong to somebody who is not looking at the screen.

// recordSeconds bounds how long one recording may run.
//
// The model's own advice is three to ten seconds: longer slows every sentence
// afterwards, because the reference sits inside every prompt, and clones
// slightly worse as well. Twelve leaves room to finish a sentence she started
// at second nine rather than cutting her off mid-word.
const (
	recordSeconds    = 12.0
	recordSilenceMS  = 1200
	recordStartLimit = 8.0
)

func (s *Server) omniRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/omnivoice/voices", s.handleOmniVoices)
	mux.HandleFunc("/api/omnivoice/record", only(http.MethodPost, s.recordOmniVoice))
	mux.HandleFunc("/api/omnivoice/upload", only(http.MethodPost, s.uploadOmniVoice))
}

func (s *Server) handleOmniVoices(writer http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodGet:
		respond(writer, http.StatusOK, omniLibrary())
	case http.MethodDelete:
		s.removeOmniVoice(writer, request)
	default:
		fail(writer, http.StatusMethodNotAllowed, "use GET or DELETE for this")
	}
}

// omniLibrary keeps a nil slice from reaching the settings app as JSON null.
// "No voices yet" is an empty list, not the absence of a list.
func omniLibrary() []omnivoice.VoiceInfo {
	library := omnivoice.Library()
	if library == nil {
		return []omnivoice.VoiceInfo{}
	}
	return library
}

// recordOmniVoice captures a few seconds through the microphone she already
// chose, and turns it into a voice.
//
// It listens on the same stream the wake word does rather than opening the
// device again. capture.Record adds a listener, so nothing is taken away from
// anything: she can be recording a voice while the wake word is still
// listening for its name, which is the only arrangement that does not require
// explaining to her that one of them had to stop.
//
// The cost of that is the rate. The microphone runs at 16 kHz because that is
// what recognition wants, and the model wants 24 -- so what it gets is a 16
// kHz recording stretched, which clones a little less well than a clean
// recording made at a higher rate. That is the trade for being able to do this
// at all without a file manager, and it is why picking a file is offered too.
func (s *Server) recordOmniVoice(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Name string `json:"name"`
		Text string `json:"text"`
	}
	if !decode(writer, request, &body) {
		return
	}

	name, err := omnivoice.CleanName(body.Name)
	if err != nil {
		fail(writer, http.StatusBadRequest, err.Error())
		return
	}

	microphone := s.engine.Microphone()
	if microphone == nil || !microphone.Running() {
		fail(writer, http.StatusConflict,
			"the microphone is not running; pick an input device first")
		return
	}

	// Said before the recording starts, not after. This is the one moment the
	// timing of the announcement is the whole of its usefulness.
	s.engine.Bus().SayKey("omnivoice.recording", feedback.Result)

	utterance := capture.Record(microphone, capture.RecorderOptions{
		// Less eager to cut than a spoken command is. A command ends on a
		// pause and should; a voice sample is somebody reading a sentence, and
		// the natural pause mid-sentence must not end it.
		Aggressiveness: 1,
		SilenceMS:      recordSilenceMS,
		MaxSeconds:     recordSeconds,
		StartTimeoutS:  recordStartLimit,
		IncludePreroll: true,
	}, request.Context().Done())

	if utterance.IsEmpty() {
		s.engine.Bus().SayKey("omnivoice.nothing_heard", feedback.Error)
		fail(writer, http.StatusBadRequest, "nothing was heard through the microphone")
		return
	}

	s.finishOmniVoice(writer, name, utterance.Duration, func() error {
		return omnivoice.Save(name, body.Text, utterance.Audio, capture.SampleRate)
	})
}

// uploadOmniVoice takes a wav file she picked rather than one recorded here.
//
// The file arrives as base64 inside JSON rather than as a multipart upload,
// because the page that sends it has no filesystem of its own -- the window's
// main process reads the file she picked and hands over the bytes. One
// encoding for one small file is worth not having a second body format in this
// API.
func (s *Server) uploadOmniVoice(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Name  string `json:"name"`
		Text  string `json:"text"`
		Audio string `json:"audio"` // base64 wav
	}
	// A dedicated reader rather than decode's: that one caps a body at four
	// megabytes, which is right for every other route here and too small for a
	// ten second recording at a rate an audio editor would have used.
	decoder := json.NewDecoder(http.MaxBytesReader(writer, request.Body, 64<<20))
	if err := decoder.Decode(&body); err != nil {
		fail(writer, http.StatusBadRequest, "could not read that file: "+err.Error())
		return
	}

	name, err := omnivoice.CleanName(body.Name)
	if err != nil {
		fail(writer, http.StatusBadRequest, err.Error())
		return
	}
	wav, err := base64.StdEncoding.DecodeString(body.Audio)
	if err != nil || len(wav) == 0 {
		fail(writer, http.StatusBadRequest, "that file did not arrive in one piece")
		return
	}

	s.finishOmniVoice(writer, name, 0, func() error {
		return omnivoice.SaveWAV(name, body.Text, wav)
	})
}

// finishOmniVoice runs the slow half and says how it went.
//
// The slow half is real: encoding a recording loads six hundred and fifty
// megabytes of model, asks it one question, and closes it again. Several
// seconds, once per recording. Both callers do exactly this afterwards, and
// the announcement is the part that must not be forgotten in one of them.
func (s *Server) finishOmniVoice(writer http.ResponseWriter,
	name string, seconds float64, save func() error) {

	started := time.Now()
	if err := save(); err != nil {
		s.engine.Bus().SayKey("omnivoice.failed", feedback.Error,
			i18n.Args{"reason": err.Error()})
		fail(writer, http.StatusBadRequest, err.Error())
		return
	}

	saved := omnivoice.VoiceInfo{Name: name}
	for _, candidate := range omnivoice.Library() {
		if candidate.Name == name {
			saved = candidate
			break
		}
	}
	if seconds == 0 {
		seconds = saved.Seconds
	}

	s.engine.Bus().SayKey("omnivoice.ready", feedback.Result, i18n.Args{"name": name})
	respond(writer, http.StatusOK, map[string]any{
		"voice":   saved,
		"seconds": seconds,
		"took_s":  time.Since(started).Seconds(),
		"voices":  omniLibrary(),
	})
}

func (s *Server) removeOmniVoice(writer http.ResponseWriter, request *http.Request) {
	name := request.URL.Query().Get("name")
	if name == "" {
		fail(writer, http.StatusBadRequest, "which voice?")
		return
	}
	if err := omnivoice.Remove(name); err != nil {
		fail(writer, http.StatusBadRequest, err.Error())
		return
	}
	respond(writer, http.StatusOK, map[string]any{"voices": omniLibrary()})
}
