// Package config is typed settings over a plain TOML file.
//
// Anything she might reasonably want to change lives here rather than in code:
// phrases, voices, devices, endpoints, languages. Loading is deliberately
// forgiving. An unknown key is a warning, not a failure, and a missing key
// keeps its default, so a config with a typo in it still starts and still
// speaks -- which is the moment she most needs it to.
//
// Secrets are kept OUT of config.toml. An API key comes from the environment
// variable named by api_key_env, or from data/secrets.toml which the settings
// page writes. That keeps config.toml safe to hand to someone for help.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/exzork/mikkilens/packages/core/paths"
)

// Error is returned when a config file exists but cannot be understood.
type Error struct{ Reason string }

func (e *Error) Error() string { return e.Reason }

// Language decides what MikkiLens speaks and what it expects to hear.
type Language struct {
	Output  string `toml:"output" json:"output"`     // spoken feedback language
	STT     string `toml:"stt" json:"stt"`           // Whisper decode language, or "auto"
	ChatTTS string `toml:"chat_tts" json:"chat_tts"` // "follow" | "auto" (detect per message)
}

// Speech covers everything about the voice that reads to her.
type Speech struct {
	Voice      string `toml:"voice" json:"voice"` // empty -> the locale default
	Rate       string `toml:"rate" json:"rate"`
	Volume     string `toml:"volume" json:"volume"`
	ChatVoice  string `toml:"chat_voice" json:"chat_voice"`   // empty -> same as Voice
	ChatRate   string `toml:"chat_rate" json:"chat_rate"`     // chat is usually wanted faster
	ChatVolume string `toml:"chat_volume" json:"chat_volume"` // empty -> same as Volume

	// The donation voice reads whatever someone paid to have read. Giving it
	// its own voice is what makes a donation recognisable without looking at
	// the screen; empty falls back to the main voice, not the chat one, so it
	// stays distinct from the stream of ordinary messages by default.
	DonationVoice  string `toml:"donation_voice" json:"donation_voice"`
	DonationRate   string `toml:"donation_rate" json:"donation_rate"`
	DonationVolume string `toml:"donation_volume" json:"donation_volume"`

	OutputDevice    string  `toml:"output_device" json:"output_device"`
	EarconVolume    float64 `toml:"earcon_volume" json:"earcon_volume"`
	ConfirmTimeoutS float64 `toml:"confirm_timeout_s" json:"confirm_timeout_s"`

	// LeadInMs is silence played before a sound when the output device has
	// gone idle. Bluetooth headphones drop the audio link when nothing is
	// playing and take a few hundred milliseconds to bring it back, losing
	// whatever was sent meanwhile -- which is how the first word of a sentence
	// disappears. Zero is right for anything wired.
	LeadInMs int `toml:"lead_in_ms" json:"lead_in_ms"`
}

// Audio covers the microphone side.
type Audio struct {
	InputDevice       string  `toml:"input_device" json:"input_device"`
	SampleRate        int     `toml:"sample_rate" json:"sample_rate"`
	VadAggressiveness int     `toml:"vad_aggressiveness" json:"vad_aggressiveness"` // 0-3
	MaxUtteranceS     float64 `toml:"max_utterance_s" json:"max_utterance_s"`
	SilenceMS         int     `toml:"silence_ms" json:"silence_ms"` // trailing silence that ends a command
}

// Wake configures the hands-free trigger word.
type Wake struct {
	Enabled   bool    `toml:"enabled" json:"enabled"`
	Model     string  `toml:"model" json:"model"`
	Threshold float64 `toml:"threshold" json:"threshold"`
	CooldownS float64 `toml:"cooldown_s" json:"cooldown_s"`
}

// Hotkey configures the push-to-talk key, which is the reliable trigger.
type Hotkey struct {
	Enabled     bool   `toml:"enabled" json:"enabled"`
	Combination string `toml:"combination" json:"combination"`
	PushToTalk  bool   `toml:"push_to_talk" json:"push_to_talk"` // false -> press once on, once off
}

// STT configures speech recognition.
type STT struct {
	Backend   string `toml:"backend" json:"backend"` // "auto" | "whispercpp" | "openai"
	ModelSize string `toml:"model_size" json:"model_size"`
	// Device is where recognition runs: "auto" uses the graphics card when
	// there is a GPU build of whisper.cpp and a driver for it, and the
	// processor otherwise. "cuda" or "gpu" asks for the card and still falls
	// back rather than leaving her with no recognition at all; "cpu" never
	// uses it.
	Device      string `toml:"device" json:"device"`
	ComputeType string `toml:"compute_type" json:"compute_type"`
	BeamSize    int    `toml:"beam_size" json:"beam_size"`
	// AutoInstall lets MikkiLens fetch a whisper.cpp build and the speech
	// model on a machine that has neither, which is every machine on the day
	// it is installed. Turned off, recognition simply reports that nothing is
	// set up -- which is the right answer for someone who has deliberately
	// put their own build in data/models, or who is on a connection where
	// half a gigabyte arriving unasked would not be welcome.
	AutoInstall bool   `toml:"auto_install" json:"auto_install"`
	Binary      string `toml:"binary" json:"binary"`         // whisper.cpp executable
	ModelPath   string `toml:"model_path" json:"model_path"` // GGML model file
	BaseURL     string `toml:"base_url" json:"base_url"`     // OpenAI-compatible transcription endpoint
	Model       string `toml:"model" json:"model"`
	APIKeyEnv   string `toml:"api_key_env" json:"api_key_env"`
}

// OBS points at the OBS Studio WebSocket server.
type OBS struct {
	Host          string  `toml:"host" json:"host"`
	Port          int     `toml:"port" json:"port"`
	Password      string  `toml:"password" json:"password"`
	MicSource     string  `toml:"mic_source" json:"mic_source"`
	AutoConnect   bool    `toml:"auto_connect" json:"auto_connect"`
	ReconnectMaxS float64 `toml:"reconnect_max_s" json:"reconnect_max_s"`
}

// YouTube covers the broadcast API and its daily quota.
//
// Nothing about connecting is configured here, because nothing about it is
// typed: the OAuth client is data/client_secret.json and the sign-in is a
// button. An API key, a channel id and a stream link used to sit here as a
// weaker second way in -- three fields somebody had to read out and somebody
// else had to paste correctly, for an account she signs into once.
type YouTube struct {
	Enabled bool `toml:"enabled" json:"enabled"`
	// Transport picks how chat is read: "auto" tries the public page, then
	// the Data API's streaming endpoint, then polling. "page" and "api"
	// restrict it to one side; "stream" and "poll" pin one Data API transport.
	Transport        string `toml:"transport" json:"transport"`
	QuotaBudget      int    `toml:"quota_budget" json:"quota_budget"`
	QuotaWarnPercent int    `toml:"quota_warn_percent" json:"quota_warn_percent"`

	// Active is the channel_id of the channel she was last on, so opening
	// MikkiLens puts her back where she left off rather than on whichever
	// channel happens to sort first.
	Active string `toml:"active,omitempty" json:"active,omitempty"`

	// Channels binds each connected YouTube channel to the OBS profile that
	// streams to it. A list of tables, [[youtube.channels]], because there is
	// more than one and there may be more later.
	Channels []Channel `toml:"channels,omitempty" json:"channels,omitempty"`
}

// Channel is one of her channels: what she calls it, which YouTube channel it
// is, and what OBS has to load to stream to it.
//
// The pairing has to be written down somewhere because nothing connects the two
// on its own. OBS knows a profile called "Music" holds a stream key; it has no
// idea which YouTube channel that key belongs to. YouTube knows the channel; it
// has never heard of OBS. This is the one place the two are the same thing, and
// it is why switching can be a single sentence she says out loud instead of two
// separate chores in two separate windows.
type Channel struct {
	// Name is what she calls it -- "main", "music" -- and what she says to
	// switch to it. Matched loosely, so it does not have to be said exactly.
	Name string `toml:"name" json:"name"`

	// ChannelID is YouTube's own id for the channel, which names the sign-in
	// file in data/youtube. Filled in by connecting; not something to type.
	ChannelID string `toml:"channel_id" json:"channel_id"`

	// Profile is the OBS profile holding this channel's stream key.
	Profile string `toml:"obs_profile" json:"obs_profile"`

	// SceneCollection is the OBS scene collection to load with it. Optional:
	// left empty, the scenes stay as they are, which is right when both
	// channels share one set of scenes and only the stream key differs.
	SceneCollection string `toml:"obs_scene_collection,omitempty" json:"obs_scene_collection,omitempty"`
}

// Named is what to call this channel out loud, falling back to something true
// rather than to an empty sentence.
func (c Channel) Named() string {
	switch {
	case c.Name != "":
		return c.Name
	case c.Profile != "":
		return c.Profile
	default:
		return c.ChannelID
	}
}

// FindChannel returns the configured channel with this YouTube channel id.
func (y YouTube) FindChannel(channelID string) (Channel, bool) {
	if channelID == "" {
		return Channel{}, false
	}
	for _, channel := range y.Channels {
		if channel.ChannelID == channelID {
			return channel, true
		}
	}
	return Channel{}, false
}

// ChannelForProfile returns the channel bound to an OBS profile name.
//
// Matched without regard to case or surrounding space, because this compares a
// name OBS holds against one written into config.toml by hand, and "music" not
// matching "Music" would be a silent failure to switch channels.
func (y YouTube) ChannelForProfile(profile string) (Channel, bool) {
	wanted := strings.ToLower(strings.TrimSpace(profile))
	if wanted == "" {
		return Channel{}, false
	}
	for _, channel := range y.Channels {
		if strings.ToLower(strings.TrimSpace(channel.Profile)) == wanted {
			return channel, true
		}
	}
	return Channel{}, false
}

// SameConnection reports whether two [youtube] sections describe the same
// connection to YouTube.
//
// Only the fields that a live sign-in depends on. The channel list and which
// channel is active change often -- every switch writes one of them -- and
// treating those as a settings change would tear the sign-in and the chat
// connection down and build them again on every switch, which is the opposite
// of what a switch is for.
func (y YouTube) SameConnection(other YouTube) bool {
	return y.Enabled == other.Enabled &&
		y.Transport == other.Transport &&
		y.QuotaBudget == other.QuotaBudget &&
		y.QuotaWarnPercent == other.QuotaWarnPercent
}

// Chat governs how live chat is read aloud.
type Chat struct {
	Enabled             bool     `toml:"enabled" json:"enabled"`
	AutostartReading    bool     `toml:"autostart_reading" json:"autostart_reading"`
	Translate           bool     `toml:"translate" json:"translate"`
	SkipEmoteOnly       bool     `toml:"skip_emote_only" json:"skip_emote_only"`
	CollapseDuplicates  bool     `toml:"collapse_duplicates" json:"collapse_duplicates"`
	ReadSuperchatsFirst bool     `toml:"read_superchats_first" json:"read_superchats_first"`
	MaxMessageChars     int      `toml:"max_message_chars" json:"max_message_chars"`
	MutedUsers          []string `toml:"muted_users" json:"muted_users"`

	// MaxGiftRecipients caps how many names are read out when somebody gifts
	// memberships in bulk. YouTube sends one message per recipient, so fifty
	// gifts is fifty announcements -- several minutes of names on a stream
	// that has just had something worth reacting to happen on it. Past the
	// cap she hears "and forty-five others" once instead. Zero reads them all.
	MaxGiftRecipients int `toml:"max_gift_recipients" json:"max_gift_recipients"`
}

// Tako is the donation overlay, watched so that chat stops being read while an
// alert is on screen.
//
// MikkiLens only listens. It never tells Tako that an alert has been played:
// that acknowledgement is what moves the queue on, and it belongs to the
// browser source in OBS that is actually showing the alerts. A second voice
// claiming to have played them would take donations off her stream.
type Tako struct {
	Enabled bool `toml:"enabled" json:"enabled"`

	// OverlayKey is the overlay_key from the alert overlay URL in the Tako
	// dashboard. Anyone holding it can read the donations as they arrive, so
	// it can be left out of config.toml and named in overlay_key_env instead.
	// Link is the whole alert overlay address from the dashboard. OverlayKey
	// takes just the key out of the middle of it, for configs written before
	// the link was accepted.
	Link          string `toml:"link" json:"link"`
	OverlayKey    string `toml:"overlay_key" json:"overlay_key"`
	OverlayKeyEnv string `toml:"overlay_key_env" json:"overlay_key_env"`

	// ReadAloud makes MikkiLens read the donation out herself instead of
	// leaving it to the overlay.
	//
	// Switch the overlay's own voice off in the site's dashboard when you turn
	// this on, or the donation is read twice at once. The point of doing it
	// here is that it uses her voice, her language and her output device, and
	// it queues with everything else she says rather than cutting across it.
	ReadAloud bool `toml:"read_aloud" json:"read_aloud"`

	// ExtraSeconds is added to every hold. Tako's own duration covers the
	// animation, not the pause either side of it, and chat starting again on
	// the exact frame the alert leaves sounds like an interruption.
	ExtraSeconds float64 `toml:"extra_seconds" json:"extra_seconds"`

	// MaxHoldS is the ceiling on one hold, however long the alert claims to
	// be. Chat that goes quiet for the rest of a stream because a number came
	// back wrong is the failure worth ruling out.
	MaxHoldS float64 `toml:"max_hold_s" json:"max_hold_s"`

	// TTSCharsPerSecond estimates how long Tako will spend reading a donation
	// message aloud, because an alert stays up until its own voice has
	// finished rather than for its configured duration. Raise it if chat comes
	// back too late, lower it if it comes back over the tail of a long
	// message.
	TTSCharsPerSecond float64 `toml:"tts_chars_per_second" json:"tts_chars_per_second"`
}

// Trakteer is the other donation overlay, watched for the same reason as Tako
// and in almost the same way.
//
// The difference is that a Trakteer donation arrives whole rather than as a
// nudge to go and fetch it, so there is no queue to race and nothing to
// acknowledge. Only the creator's overlay settings are read over HTTP, and
// only to learn how long an alert stays up.
type Trakteer struct {
	Enabled bool `toml:"enabled" json:"enabled"`

	// Link is the whole notification overlay address from the dashboard. Both
	// the key and the hash are in it and both are needed, so this takes the
	// address rather than making her pick it apart by hand.
	//
	// Anyone holding it can read the donations as they arrive, so it can be
	// left out of config.toml and named in link_env instead.
	Link    string `toml:"link" json:"link"`
	LinkEnv string `toml:"link_env" json:"link_env"`

	// ReadAloud makes MikkiLens read the donation out herself instead of
	// leaving it to the overlay.
	//
	// Switch the overlay's own voice off in the site's dashboard when you turn
	// this on, or the donation is read twice at once. The point of doing it
	// here is that it uses her voice, her language and her output device, and
	// it queues with everything else she says rather than cutting across it.
	ReadAloud bool `toml:"read_aloud" json:"read_aloud"`

	// The same three adjustments as Tako, and for the same reasons.
	ExtraSeconds      float64 `toml:"extra_seconds" json:"extra_seconds"`
	MaxHoldS          float64 `toml:"max_hold_s" json:"max_hold_s"`
	TTSCharsPerSecond float64 `toml:"tts_chars_per_second" json:"tts_chars_per_second"`
}

// Vision is how the screen is captured before it is sent. Which model looks at
// it is not a question this section answers -- there is one model, and it is
// configured in [model].
type Vision struct {
	MaxEdge        int    `toml:"max_edge" json:"max_edge"`
	Monitors       string `toml:"monitors" json:"monitors"` // "all" | "primary" | a 1-based index
	MaxAnswerChars int    `toml:"max_answer_chars" json:"max_answer_chars"`
}

// Model is the one OpenAI-compatible endpoint MikkiLens talks to.
//
// One endpoint, one model, for every kind of ask: summarising chat, working
// out what an unrecognised command meant, and describing the screen. Three
// sections meant three places to paste a key into and three ways to have
// pasted it wrong, for a distinction nobody using this application was making.
//
// It follows that the model has to be one that can see. Every provider worth
// pointing this at answers on the same endpoint whether the message carries an
// image or not, so this costs nothing but choosing a multimodal model -- and
// describing the screen is the feature this application exists for.
type Model struct {
	Base      string  `toml:"base_url" json:"base_url"`
	Model     string  `toml:"model" json:"model"`
	APIKeyEnv string  `toml:"api_key_env" json:"api_key_env"`
	TimeoutS  float64 `toml:"timeout_s" json:"timeout_s"`
}

// Configured reports whether the model can actually be called.
func (m Model) Configured() bool { return m.Base != "" && m.Model != "" }

// UI is the local API the desktop app talks to.
type UI struct {
	Host        string `toml:"host" json:"host"`
	Port        int    `toml:"port" json:"port"`
	OpenOnStart bool   `toml:"open_on_start" json:"open_on_start"`
	LanAccess   bool   `toml:"lan_access" json:"lan_access"` // opt-in; lets someone help remotely
}

// Binding fires one command from a key, with nothing said.
//
// Every device she might reach for presents as an ordinary key combination:
// a Stream Deck key of any brand, a foot pedal, a mouse macro, a second
// keyboard. So one mechanism covers all of them, and none of them needs to
// know MikkiLens exists.
type Binding struct {
	Combination string `toml:"combination" json:"combination"`
	Command     string `toml:"command" json:"command"`

	// Confirm overrides the command's own gate. Unset leaves it alone, so a
	// bound key that ends the stream still asks first and is answered out
	// loud. Setting it false is how a dedicated key becomes a single press --
	// worth having on a key that cannot be pressed by accident, and worth
	// thinking about on one that can.
	Confirm *bool `toml:"confirm,omitempty" json:"confirm,omitempty"`
}

// Config is every setting, in one value that can be copied and swapped live.
type Config struct {
	Language Language `toml:"language" json:"language"`
	Speech   Speech   `toml:"speech" json:"speech"`
	Audio    Audio    `toml:"audio" json:"audio"`
	Wake     Wake     `toml:"wake" json:"wake"`
	Hotkey   Hotkey   `toml:"hotkey" json:"hotkey"`
	STT      STT      `toml:"stt" json:"stt"`
	OBS      OBS      `toml:"obs" json:"obs"`
	YouTube  YouTube  `toml:"youtube" json:"youtube"`
	Chat     Chat     `toml:"chat" json:"chat"`
	Tako     Tako     `toml:"tako" json:"tako"`
	Trakteer Trakteer `toml:"trakteer" json:"trakteer"`
	Model    Model    `toml:"model" json:"model"`
	Vision   Vision   `toml:"vision" json:"vision"`
	Matcher  Matcher  `toml:"matcher" json:"matcher"`
	UI       UI       `toml:"ui" json:"ui"`

	// Bindings is a list of tables rather than a section, because the same
	// key name repeats: [[bindings]] once per key. Left out when empty, so a
	// config nobody has bound a key in stays a config about her voice.
	Bindings []Binding `toml:"bindings,omitempty" json:"bindings,omitempty"`
}

// Default is the configuration MikkiLens runs with when nothing is set.
func Default() Config {
	return Config{
		Language: Language{Output: "id", STT: "id", ChatTTS: "follow"},
		Speech: Speech{
			Rate: "+0%", Volume: "+0%", ChatRate: "+15%", ChatVolume: "+0%",
			DonationRate: "+0%", DonationVolume: "+0%",
			EarconVolume: 0.25, ConfirmTimeoutS: 8.0,
			LeadInMs: 300,
		},
		Audio: Audio{
			SampleRate: 16000, VadAggressiveness: 2,
			MaxUtteranceS: 12.0, SilenceMS: 700,
		},
		// 0.8 rather than 0.6: the threshold was chosen from a measured curve
		// rather than guessed. At 0.8 the model answers 84% of utterances put
		// through rooms and noise, and fires 0.28 times an hour on eleven
		// hours of recorded conversation; at 0.6 it answers 91% and fires 1.6
		// times an hour. Mid-stream, the quieter one is the right trade.
		// The name is spelled out rather than taken from wake.Builtin: config
		// is imported by nearly everything, and the wake package drags the
		// ONNX runtime and a C toolchain in behind it. wake.TestBuiltinIsTheConfigDefault
		// keeps the two honest.
		Wake:   Wake{Enabled: true, Model: "mikkilens", Threshold: 0.8, CooldownS: 2.0},
		Hotkey: Hotkey{Enabled: true, Combination: "<ctrl>+<alt>+<space>", PushToTalk: true},
		STT: STT{
			// small rather than base: base mishears enough of a short
			// Indonesian command to be the difference between a command that
			// works and one she has to repeat, and small costs about a
			// gigabyte of memory to fix that. Beam size 0 means "decide from
			// where it runs" -- wide on a graphics card, narrow on a
			// processor.
			Backend: "auto", ModelSize: "small", Device: "auto",
			ComputeType: "auto", BeamSize: 0, APIKeyEnv: "MIKKILENS_STT_KEY",
			AutoInstall: true,
		},
		OBS: OBS{
			Host: "localhost", Port: 4455, MicSource: "Mic/Aux",
			AutoConnect: true, ReconnectMaxS: 30.0,
		},
		YouTube: YouTube{
			Enabled: true, Transport: "auto",
			QuotaBudget: 10000, QuotaWarnPercent: 80,
		},
		Chat: Chat{
			Enabled: true, AutostartReading: true, SkipEmoteOnly: true,
			CollapseDuplicates: true, ReadSuperchatsFirst: true,
			MaxMessageChars: 200, MutedUsers: []string{},
			MaxGiftRecipients: 5,
		},
		Tako: Tako{
			OverlayKeyEnv: "MIKKILENS_TAKO_OVERLAY_KEY",
			ExtraSeconds:  2.0, MaxHoldS: 90.0, TTSCharsPerSecond: 14.0,
		},
		Trakteer: Trakteer{
			LinkEnv:      "MIKKILENS_TRAKTEER_LINK",
			ExtraSeconds: 2.0, MaxHoldS: 90.0, TTSCharsPerSecond: 14.0,
		},
		Model:   Model{APIKeyEnv: "MIKKILENS_MODEL_KEY", TimeoutS: 30.0},
		Vision:  Vision{MaxEdge: 1568, Monitors: "all", MaxAnswerChars: 700},
		Matcher: Matcher{Enabled: true},
		UI:      UI{Host: "127.0.0.1", Port: 8760},
	}
}

// Load reads a config file, or returns the defaults when there is none.
func Load(path string) (Config, error) {
	if path == "" {
		path = paths.ConfigFile()
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		slog.Info("no config file, using defaults", "path", path)
		return Default(), nil
	}
	if err != nil {
		return Default(), &Error{Reason: err.Error()}
	}

	var document map[string]any
	if err := toml.Unmarshal(data, &document); err != nil {
		slog.Error("config file is not valid TOML", "path", path, "error", err)
		return Default(), &Error{Reason: err.Error()}
	}
	return FromMap(document), nil
}

// FromMap builds a config from a decoded document, ignoring what it does not
// recognise. Unknown sections and keys are warned about rather than fatal.
func FromMap(document map[string]any) Config {
	settings := Default()

	known := sectionFields(settings)
	for section, body := range document {
		fields, ok := known[section]
		if !ok {
			slog.Warn("ignoring unknown config section", "section", section)
			continue
		}
		table, ok := body.(map[string]any)
		if !ok {
			continue
		}
		for key := range table {
			if !fields[key] {
				slog.Warn("ignoring unknown config key", "section", section, "key", key)
			}
		}
	}

	// Re-encode so go-toml applies the values it understands on top of the
	// defaults, leaving anything absent exactly as Default() left it.
	encoded, err := toml.Marshal(document)
	if err != nil {
		slog.Error("could not re-encode configuration", "error", err)
		return settings
	}
	if err := toml.Unmarshal(encoded, &settings); err != nil {
		slog.Error("bad values in configuration, keeping defaults", "error", err)
		return Default()
	}
	return settings
}

// sectionFields maps each section name to the keys it accepts.
func sectionFields(settings Config) map[string]map[string]bool {
	sections := map[string]map[string]bool{}
	outer := reflect.TypeOf(settings)
	for i := 0; i < outer.NumField(); i++ {
		field := outer.Field(i)
		name := field.Tag.Get("toml")
		if name == "" {
			continue
		}
		if field.Type.Kind() != reflect.Struct {
			// A list of tables, like [[bindings]]. It is recognised, but it
			// has no fixed set of keys to check, so nil stands for "known,
			// nothing to warn about".
			sections[name] = nil
			continue
		}

		keys := map[string]bool{}
		inner := field.Type
		for j := 0; j < inner.NumField(); j++ {
			if key := inner.Field(j).Tag.Get("toml"); key != "" {
				keys[key] = true
			}
		}
		sections[name] = keys
	}
	return sections
}

// ToMap renders the config as a plain document, which is what the settings API
// hands to the desktop app.
func (c Config) ToMap() map[string]any {
	encoded, err := toml.Marshal(c)
	if err != nil {
		return map[string]any{}
	}
	document := map[string]any{}
	_ = toml.Unmarshal(encoded, &document)
	return document
}

// Save writes the config atomically: a half-written file would leave her with
// a machine that cannot start and no way to see why.
func (c Config) Save(path string) (string, error) {
	if path == "" {
		path = paths.ConfigFile()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return path, err
	}
	encoded, err := toml.Marshal(c)
	if err != nil {
		return path, err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, encoded, 0o644); err != nil {
		return path, err
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return path, err
	}
	return path, nil
}

// Voice is the configured voice, or the locale default when none is set.
func (c Config) Voice(localeDefault string) string {
	if c.Speech.Voice != "" {
		return c.Speech.Voice
	}
	return localeDefault
}

// VoiceForChat is the chat voice, falling back to the main voice.
func (c Config) VoiceForChat(localeDefault string) string {
	if c.Speech.ChatVoice != "" {
		return c.Speech.ChatVoice
	}
	return c.Voice(localeDefault)
}

// VoiceForDonation is the donation voice, falling back to the main voice
// rather than the chat voice: a donation should not sound like chat.
func (c Config) VoiceForDonation(localeDefault string) string {
	if c.Speech.DonationVoice != "" {
		return c.Speech.DonationVoice
	}
	return c.Voice(localeDefault)
}

// Matcher is the fallback that works out what she meant when none of the
// written phrases matched.
//
// A switch rather than an endpoint of its own. It asks the same model as
// everything else, so the only thing left to decide is whether unrecognised
// speech is sent at all -- which is a real decision and hers to make: with
// this on, whatever the phrases miss goes to the provider in [model].
type Matcher struct {
	Enabled bool `toml:"enabled" json:"enabled"`
}

// ModelEndpoint is the one provider MikkiLens calls, whatever it is asking.
func (c Config) ModelEndpoint() (base, model, key string) {
	return c.Model.Base, c.Model.Model, ResolveSecret(c.Model.APIKeyEnv)
}

// ModelAPIKey resolves the key for that provider.
func (c Config) ModelAPIKey() string { return ResolveSecret(c.Model.APIKeyEnv) }

// STTAPIKey resolves the key for a remote transcription endpoint.
func (c Config) STTAPIKey() string { return ResolveSecret(c.STT.APIKeyEnv) }

// TakoOverlaySource is the alert overlay link, or the bare key, whichever was
// configured, and from the environment or data/secrets.toml when config.toml
// would rather not carry it. Turning it into a key is the tako package's job:
// it is the one that knows what the address looks like.
func (c Config) TakoOverlaySource() string {
	if link := strings.TrimSpace(c.Tako.Link); link != "" {
		return link
	}
	if key := strings.TrimSpace(c.Tako.OverlayKey); key != "" {
		return key
	}
	return strings.TrimSpace(ResolveSecret(c.Tako.OverlayKeyEnv))
}

// TrakteerLink is the notification overlay address, taken from the environment
// or data/secrets.toml when config.toml would rather not carry it.
func (c Config) TrakteerLink() string {
	if link := strings.TrimSpace(c.Trakteer.Link); link != "" {
		return link
	}
	return strings.TrimSpace(ResolveSecret(c.Trakteer.LinkEnv))
}

// Describe is a one-line summary used in logs and the self test.
func (c Config) Describe() string {
	return fmt.Sprintf("language=%s stt=%s voice=%s obs=%s:%d",
		c.Language.Output, c.Language.STT, c.Speech.Voice, c.OBS.Host, c.OBS.Port)
}
