package config

import (
	"log/slog"
	"math"
	"strconv"
	"strings"
)

// Volumes are whole percentages, 0 to 100, of one particular sound's own level
// -- her voice, the tones, the song. Never of the machine's: the system volume
// carries the game and whatever OBS is capturing too, so turning her down
// there turns the stream down with her.
//
// This file is what keeps that one shape true for a config file written by an
// older version, and for the settings page, which speaks JSON and therefore
// has no whole numbers at all.

// volumeKeys is every volume in the file, by the section it lives in. The
// speech volume comes first because an empty chat or donation volume used to
// mean "the same as that one".
var volumeKeys = map[string][]string{
	"speech": {"volume", "chat_volume", "donation_volume", "earcon_volume"},
	"music":  {"volume", "duck_volume"},
}

// ClampPercent holds a volume inside 0 to 100.
func ClampPercent(value int) int {
	if value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}

// migrateVolumes rewrites the two shapes volumes used to be written in, so
// that upgrading never quietly changes how loud she is.
//
// Speech volumes were the relative percentages the online voice was asked for:
// "-40%" meant forty percent below the voice's own level, and came back at
// sixty percent of its amplitude, so it becomes 60. The tones and the music
// were fractions of one. A "+10%" and anything else above the voice's own
// level becomes 100: the gain applied here cannot go above one without
// clipping, and a request to be louder than everything is a request to be as
// loud as possible.
//
// Only for a document read from disk. The settings page sends JSON, where
// every number is a float and 1 means one percent rather than everything.
func migrateVolumes(document map[string]any) {
	eachVolume(document, func(table map[string]any, key string, value any) {
		switch typed := value.(type) {
		case string:
			if strings.TrimSpace(typed) == "" {
				// Empty meant "as loud as the voice". Kept rather than
				// defaulted, because it is what she has been hearing.
				if follow, ok := table["volume"].(int64); ok {
					table[key] = follow
					return
				}
				delete(table, key)
				return
			}
			if percent, ok := parsePercent(typed); ok {
				table[key] = percent
				return
			}
			delete(table, key)
		case float64:
			// A fraction of one, which is how the tones and the music were
			// written. 1.0 was everything, not one percent.
			if typed > 0 && typed <= 1 {
				table[key] = int64(math.Round(typed * 100))
			}
		}
	})
}

// coerceVolumes turns whatever a volume arrived as into a whole percentage
// inside 0 to 100.
//
// Mostly for the settings page: JSON has one number type, so 60 arrives as
// 60.0, and go-toml will not put a float into an int field. A value that
// cannot be read at all is dropped rather than kept, so one unreadable volume
// costs its own default instead of the whole configuration.
func coerceVolumes(document map[string]any) {
	eachVolume(document, func(table map[string]any, key string, value any) {
		percent, ok := asPercent(value)
		if !ok {
			slog.Warn("ignoring a volume that is not a number", "key", key, "value", value)
			delete(table, key)
			return
		}
		table[key] = percent
	})
}

func eachVolume(document map[string]any, visit func(table map[string]any, key string, value any)) {
	for section, keys := range volumeKeys {
		table, ok := document[section].(map[string]any)
		if !ok {
			continue
		}
		for _, key := range keys {
			if value, present := table[key]; present {
				visit(table, key, value)
			}
		}
	}
}

// asPercent reads a volume out of any of the types a document can hold.
func asPercent(value any) (int64, bool) {
	switch typed := value.(type) {
	case int64:
		return clamp64(typed), true
	case int:
		return clamp64(int64(typed)), true
	case float64:
		return clamp64(int64(math.Round(typed))), true
	case float32:
		return clamp64(int64(math.Round(float64(typed)))), true
	case string:
		return parsePercent(typed)
	}
	return 0, false
}

// parsePercent reads "60", "60%", and the relative "+10%" and "-40%" an older
// config or an older settings page writes.
func parsePercent(text string) (int64, bool) {
	text = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(text), "%"))
	if text == "" {
		return 0, false
	}
	number, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return 0, false
	}
	if text[0] == '+' || text[0] == '-' {
		// Relative to the voice's own level, which was full volume.
		number += 100
	}
	return clamp64(int64(math.Round(number))), true
}

func clamp64(value int64) int64 { return int64(ClampPercent(int(value))) }
