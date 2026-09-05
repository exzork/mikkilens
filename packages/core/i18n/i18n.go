// Package i18n loads the strings MikkiLens speaks.
//
// A missing key must never produce silence: when the only answer arrives by
// ear, silence is indistinguishable from a crash. So lookups fall back to
// English, and then to a spoken marker naming the key, which is at least
// audible and diagnosable.
//
// The locale files are embedded in the binary so a fresh install always has a
// voice, but a matching file under <root>/locales wins if she wants to reword
// something herself.
package i18n

import (
	"embed"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/pelletier/go-toml/v2"

	"github.com/exzork/mikkilens/packages/core/paths"
)

//go:embed locales/*.toml
var embedded embed.FS

// FallbackLanguage backs every other locale, so a gap in a translation is
// spoken in English rather than not spoken at all.
const FallbackLanguage = "en"

// Args carries the placeholder values for a string, as in {scene} or {count}.
type Args map[string]any

// Available lists the languages that can be loaded, embedded and on disk.
func Available() []string {
	found := map[string]bool{}
	if entries, err := embedded.ReadDir("locales"); err == nil {
		for _, entry := range entries {
			found[strings.TrimSuffix(entry.Name(), ".toml")] = true
		}
	}
	if entries, err := os.ReadDir(paths.LocalesDir()); err == nil {
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), ".toml") {
				found[strings.TrimSuffix(entry.Name(), ".toml")] = true
			}
		}
	}
	languages := make([]string, 0, len(found))
	for language := range found {
		languages = append(languages, language)
	}
	sort.Strings(languages)
	return languages
}

// Meta is the [meta] table: everything about a language that is not a line
// MikkiLens speaks.
type Meta struct {
	Name  string   `toml:"name"`
	Code  string   `toml:"code"`
	Voice string   `toml:"voice"`
	Yes   []string `toml:"yes"`
	No    []string `toml:"no"`
}

// Locale is the strings for one language, with English behind it.
type Locale struct {
	Language string
	Strings  map[string]string
	Meta     Meta

	fallback map[string]string
	mu       sync.Mutex
	missing  map[string]bool
}

// Load reads one language, falling back to English when it is not there.
func Load(language string) *Locale {
	raw, meta, err := loadRaw(language)
	if err != nil {
		slog.Warn("locale not found, falling back", "language", language, "fallback", FallbackLanguage)
		language = FallbackLanguage
		raw, meta, err = loadRaw(language)
		if err != nil {
			// The English file ships inside the binary, so reaching here means
			// the build itself is broken.
			slog.Error("no usable locale at all", "error", err)
			raw, meta = map[string]string{}, Meta{}
		}
	}

	locale := &Locale{
		Language: language,
		Strings:  raw,
		Meta:     meta,
		missing:  map[string]bool{},
	}
	if language != FallbackLanguage {
		if fallback, _, err := loadRaw(FallbackLanguage); err == nil {
			locale.fallback = fallback
		}
	}
	return locale
}

func loadRaw(language string) (map[string]string, Meta, error) {
	name := language + ".toml"
	data, err := os.ReadFile(filepath.Join(paths.LocalesDir(), name))
	if err != nil {
		data, err = fs.ReadFile(embedded, "locales/"+name)
		if err != nil {
			return nil, Meta{}, err
		}
	}

	var document map[string]any
	if err := toml.Unmarshal(data, &document); err != nil {
		return nil, Meta{}, err
	}

	var wrapper struct {
		Meta Meta `toml:"meta"`
	}
	_ = toml.Unmarshal(data, &wrapper)

	return flatten(document), wrapper.Meta, nil
}

// flatten turns {"obs": {"connected": "x"}} into {"obs.connected": "x"} and
// skips [meta], which is data rather than speech.
func flatten(document map[string]any) map[string]string {
	flat := map[string]string{}
	for section, body := range document {
		if section == "meta" {
			continue
		}
		table, ok := body.(map[string]any)
		if !ok {
			continue
		}
		for key, value := range table {
			if text, ok := value.(string); ok {
				flat[section+"."+key] = text
			}
		}
	}
	return flat
}

// Has reports whether a key resolves, here or in the fallback.
func (l *Locale) Has(key string) bool {
	if _, ok := l.Strings[key]; ok {
		return true
	}
	_, ok := l.fallback[key]
	return ok
}

// T resolves a key to something speakable. It never returns an empty string.
func (l *Locale) T(key string, args ...Args) string {
	template, ok := l.Strings[key]
	if !ok || template == "" {
		template, ok = l.fallback[key]
	}
	if !ok || template == "" {
		l.mu.Lock()
		l.missing[key] = true
		l.mu.Unlock()
		slog.Error("missing locale key", "key", key, "language", l.Language)
		// Dots and underscores read badly aloud, so say the key as words.
		spoken := strings.NewReplacer(".", " ", "_", " ").Replace(key)
		return "Missing text for " + spoken
	}
	return format(template, merge(args))
}

// Resolve treats value as a locale key when it is one, and as literal text
// otherwise, so commands.toml can write either a key or a whole sentence
// without the caller needing to know which it got.
func (l *Locale) Resolve(value string, args ...Args) string {
	if l.Has(value) {
		return l.T(value, args...)
	}
	return format(value, merge(args))
}

// MissingKeys lists every key that was asked for and did not exist.
func (l *Locale) MissingKeys() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	keys := make([]string, 0, len(l.missing))
	for key := range l.missing {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// DefaultVoice is the voice this language is read in unless config says otherwise.
func (l *Locale) DefaultVoice() string {
	if l.Meta.Voice != "" {
		return l.Meta.Voice
	}
	return "en-US-AriaNeural"
}

// DisplayName is how the language is named to a person.
func (l *Locale) DisplayName() string {
	if l.Meta.Name != "" {
		return l.Meta.Name
	}
	return l.Language
}

// YesWords and NoWords are matched against a spoken confirmation, so a
// destructive command is answered in her own language rather than in English.
func (l *Locale) YesWords() []string {
	if len(l.Meta.Yes) == 0 {
		return []string{"yes"}
	}
	return l.Meta.Yes
}

func (l *Locale) NoWords() []string {
	if len(l.Meta.No) == 0 {
		return []string{"no"}
	}
	return l.Meta.No
}

func merge(args []Args) Args {
	if len(args) == 1 {
		return args[0]
	}
	merged := Args{}
	for _, set := range args {
		for key, value := range set {
			merged[key] = value
		}
	}
	return merged
}
