//go:build !windows

package tts

import "context"

// SynthesizeSAPI has no equivalent away from Windows. MikkiLens targets a
// Windows streaming machine; elsewhere the online voice is the only voice, and
// saying so plainly beats pretending there is a fallback.
func SynthesizeSAPI(_ context.Context, _ string, _ int) (Audio, error) {
	return Audio{}, failure("no offline voice is available on this platform")
}
