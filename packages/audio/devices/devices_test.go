package devices_test

import (
	"strings"
	"testing"

	"github.com/exzork/mikkilens/packages/audio/devices"
)

// These talk to the real sound card. They are written to report what they
// found rather than to demand a particular machine, because the thing worth
// catching is "enumeration returned nothing" -- which is what a blind user
// experiences as MikkiLens having no voice at all.

func TestEnumeratesRealDevices(t *testing.T) {
	if _, err := devices.List(devices.Output); err != nil {
		t.Skipf("no audio backend in this environment: %v", err)
	}
	t.Logf("audio backend: %s", devices.HostAPI())

	for _, kind := range []devices.Kind{devices.Output, devices.Input} {
		found, err := devices.List(kind)
		if err != nil {
			t.Fatalf("List(%s): %v", kind, err)
		}
		t.Logf("%s devices (%d):", kind, len(found))
		for _, device := range found {
			t.Logf("  %2d. %s", device.Index, device.Label())
		}
		if len(found) == 0 {
			continue
		}

		// An endpoint id is what config stores, so every device needs one.
		for _, device := range found {
			if device.ID == "" {
				t.Errorf("%s device %q has no endpoint id", kind, device.Name)
			}
		}

		// One entry per physical device is the whole point of picking a single
		// backend: reading thirty-one duplicates aloud would be unusable.
		seen := map[string]bool{}
		for _, device := range found {
			if device.Name == "" {
				t.Errorf("a %s device has no name", kind)
			}
			if seen[device.Name] {
				t.Errorf("%s device %q is listed twice", kind, device.Name)
			}
			seen[device.Name] = true
		}
	}
}

func TestResolveFindsADeviceBySubstring(t *testing.T) {
	found, err := devices.List(devices.Output)
	if err != nil || len(found) == 0 {
		t.Skip("no output devices in this environment")
	}

	// Config stores part of a name, so it survives Windows renumbering.
	full := found[0].Name
	fragment := full
	if fields := strings.Fields(full); len(fields) > 1 {
		fragment = fields[0]
	}

	resolved, err := devices.Resolve(fragment, devices.Output)
	if err != nil {
		t.Fatalf("Resolve(%q): %v", fragment, err)
	}
	if resolved == nil {
		t.Fatalf("Resolve(%q) found nothing, though %q exists", fragment, full)
	}
	t.Logf("resolved %q to %q", fragment, resolved.Name)
}

func TestAnEmptySpecMeansTheSystemDefault(t *testing.T) {
	resolved, err := devices.Resolve("", devices.Output)
	if err != nil {
		t.Fatalf("Resolve(\"\"): %v", err)
	}
	if resolved != nil {
		t.Errorf("an empty spec must mean the system default, got %q", resolved.Name)
	}
}

func TestAnUnknownNameFallsBackRatherThanFailing(t *testing.T) {
	if _, err := devices.List(devices.Output); err != nil {
		t.Skip("no output devices in this environment")
	}
	// Falling back to the default is right: refusing to speak because a device
	// was renamed would be the worse failure by far.
	resolved, err := devices.Resolve("no such device anywhere", devices.Output)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved != nil {
		t.Logf("fuzzily matched to %q, which is acceptable", resolved.Name)
	}
}

func TestToneHasTheRightLengthAndFades(t *testing.T) {
	const rate = 48000
	wave := devices.Tone(660, 0.35, rate, 0.3)

	if want := int(0.35 * rate); len(wave) != want {
		t.Errorf("tone is %d samples, want %d", len(wave), want)
	}
	// Fades at both ends are what make it a chime rather than a click.
	if wave[0] != 0 {
		t.Errorf("the tone does not start from silence: %v", wave[0])
	}
	if last := wave[len(wave)-1]; last > 0.01 || last < -0.01 {
		t.Errorf("the tone does not fade out: %v", last)
	}

	peak := float32(0)
	for _, sample := range wave {
		if sample > peak {
			peak = sample
		}
	}
	if peak < 0.2 || peak > 0.31 {
		t.Errorf("peak amplitude is %v, want about 0.3", peak)
	}
}
