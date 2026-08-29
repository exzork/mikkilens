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
	Voice           string  `toml:"voice" json:"voice"` // empty -> the locale default
	Rate            string  `toml:"rate" json:"rate"`
	Volume          string  `toml:"volume" json:"volume"`
	ChatVoice       string  `toml:"chat_voice" json:"chat_voice"` // empty -> same as Voice
	ChatRate        string  `toml:"chat_rate" json:"chat_rate"`   // chat is usually wanted faster
	OutputDevice    string  `toml:"output_device" json:"output_device"`
	EarconVolume    float64 `toml:"earcon_volume" json:"earcon_volume"`
	ConfirmTimeoutS float64 `toml:"confirm_timeout_s" json:"confirm_timeout_s"`
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
	Backend     string `toml:"backend" json:"backend"` // "auto" | "whispercpp" | "openai"
	ModelSize   string `toml:"model_size" json:"model_size"`
	Device      string `toml:"device" json:"device"` // "auto" | "cuda" | "cpu"
	ComputeType string `toml:"compute_type" json:"compute_type"`
	BeamSize    int    `toml:"beam_size" json:"beam_size"`
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
type YouTube struct {
	Enabled          bool   `toml:"enabled" json:"enabled"`
	Transport        string `toml:"transport" json:"transport"` // "auto" | "stream" | "poll"
	QuotaBudget      int    `toml:"quota_budget" json:"quota_budget"`
	QuotaWarnPercent int    `toml:"quota_warn_percent" json:"quota_warn_percent"`
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
}

// Vision is the screen-description model. Any OpenAI-compatible endpoint works.
type Vision struct {
	Base           string  `toml:"base_url" json:"base_url"`
	Model          string  `toml:"model" json:"model"`
	APIKeyEnv      string  `toml:"api_key_env" json:"api_key_env"`
	MaxEdge        int     `toml:"max_edge" json:"max_edge"`
	TimeoutS       float64 `toml:"timeout_s" json:"timeout_s"`
	Monitors       string  `toml:"monitors" json:"monitors"` // "all" | "primary" | a 1-based index
	MaxAnswerChars int     `toml:"max_answer_chars" json:"max_answer_chars"`
}

// Configured reports whether the vision model can actually be called.
func (v Vision) Configured() bool { return v.Base != "" && v.Model != "" }

// LLM is the text model for chat summaries. Empty fields fall back to [vision].
type LLM struct {
	Base      string `toml:"base_url" json:"base_url"`
	Model     string `toml:"model" json:"model"`
	APIKeyEnv string `toml:"api_key_env" json:"api_key_env"`
}

// UI is the local API the desktop app talks to.
type UI struct {
	Host        string `toml:"host" json:"host"`
	Port        int    `toml:"port" json:"port"`
	OpenOnStart bool   `toml:"open_on_start" json:"open_on_start"`
	LanAccess   bool   `toml:"lan_access" json:"lan_access"` // opt-in; lets someone help remotely
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
	Vision   Vision   `toml:"vision" json:"vision"`
	LLM      LLM      `toml:"llm" json:"llm"`
	UI       UI       `toml:"ui" json:"ui"`
}

// Default is the configuration MikkiLens runs with when nothing is set.
func Default() Config {
	return Config{
		Language: Language{Output: "id", STT: "id", ChatTTS: "follow"},
		Speech: Speech{
			Rate: "+0%", Volume: "+0%", ChatRate: "+15%",
			EarconVolume: 0.25, ConfirmTimeoutS: 8.0,
		},
		Audio: Audio{
			SampleRate: 16000, VadAggressiveness: 2,
			MaxUtteranceS: 12.0, SilenceMS: 700,
		},
		Wake:   Wake{Enabled: true, Model: "hey_jarvis", Threshold: 0.6, CooldownS: 2.0},
		Hotkey: Hotkey{Enabled: true, Combination: "<ctrl>+<alt>+<space>", PushToTalk: true},
		STT: STT{
			Backend: "auto", ModelSize: "small", Device: "auto",
			ComputeType: "auto", BeamSize: 1, APIKeyEnv: "MIKKILENS_STT_KEY",
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
		},
		Vision: Vision{
			APIKeyEnv: "MIKKILENS_VISION_KEY", MaxEdge: 1568,
			TimeoutS: 30.0, Monitors: "all", MaxAnswerChars: 700,
		},
		UI: UI{Host: "127.0.0.1", Port: 8760},
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

// LLMEndpoint is the text model to use, falling back to the vision provider so
// one endpoint can serve both.
func (c Config) LLMEndpoint() (base, model, key string) {
	base = firstNonEmpty(c.LLM.Base, c.Vision.Base)
	model = firstNonEmpty(c.LLM.Model, c.Vision.Model)
	return base, model, ResolveSecret(firstNonEmpty(c.LLM.APIKeyEnv, c.Vision.APIKeyEnv))
}

// VisionAPIKey resolves the key for the vision provider.
func (c Config) VisionAPIKey() string { return ResolveSecret(c.Vision.APIKeyEnv) }

// STTAPIKey resolves the key for a remote transcription endpoint.
func (c Config) STTAPIKey() string { return ResolveSecret(c.STT.APIKeyEnv) }

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// Describe is a one-line summary used in logs and the self test.
func (c Config) Describe() string {
	return fmt.Sprintf("language=%s stt=%s voice=%s obs=%s:%d",
		c.Language.Output, c.Language.STT, c.Speech.Voice, c.OBS.Host, c.OBS.Port)
}
