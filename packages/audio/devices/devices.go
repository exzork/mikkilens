// Package devices enumerates and selects audio hardware.
//
// Windows exposes every physical device once per backend -- WASAPI,
// DirectSound, WinMM -- which on a normal machine turns seven real devices
// into thirty-one entries, with WinMM truncating the names to 31 characters.
// Reading that list aloud would be unusable, so MikkiLens speaks to WASAPI
// directly: one entry per device, full names, and the lowest latency.
package devices

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/exzork/mikkilens/packages/audio/wasapi"
	"github.com/exzork/mikkilens/packages/core/fuzzy"
)

// Kind is which half of the sound card we mean.
type Kind string

const (
	Input  Kind = "input"
	Output Kind = "output"
)

// ErrNoDevice means no usable device of the requested kind exists.
var ErrNoDevice = errors.New("no audio device found")

func direction(kind Kind) wasapi.Direction {
	if kind == Input {
		return wasapi.Capture
	}
	return wasapi.Render
}

// Device is one piece of audio hardware, as MikkiLens presents it.
type Device struct {
	// Index is this device's position in the list, which is all the settings
	// app needs to name one when pressing Test.
	Index int `json:"index"`

	// ID is the endpoint identifier Windows gave it. It survives reboots and
	// renumbering, which is what makes it safe to hold on to.
	ID string `json:"id"`

	// Name is what a person would recognise, such as
	// "Speakers (G733 Gaming Headset)".
	Name string `json:"name"`

	IsDefault bool `json:"is_default"`
	Kind      Kind `json:"kind"`
}

// Label is how the device is announced aloud and shown in the settings app.
func (d Device) Label() string {
	if d.IsDefault {
		return d.Name + " (default)"
	}
	return d.Name
}

// MarshalJSON adds the label, because every caller that sends a device
// somewhere wants to show it.
func (d Device) MarshalJSON() ([]byte, error) {
	return []byte(fmt.Sprintf(
		`{"index":%d,"id":%q,"name":%q,"label":%q,"host_api":"WASAPI","is_default":%t,"kind":%q}`,
		d.Index, d.ID, d.Name, d.Label(), d.IsDefault, d.Kind,
	)), nil
}

// HostAPI names the audio backend in use. There is only one now, but the
// settings app still shows it, and it is worth being able to see.
func HostAPI() string { return "Windows WASAPI" }

// List returns the devices of one kind, one entry per physical device.
func List(kind Kind) ([]Device, error) {
	endpoints, err := wasapi.Endpoints(direction(kind))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNoDevice, err)
	}

	devices := make([]Device, 0, len(endpoints))
	for index, endpoint := range endpoints {
		devices = append(devices, Device{
			Index:     index,
			ID:        endpoint.ID,
			Name:      strings.TrimSpace(endpoint.Name),
			IsDefault: endpoint.IsDefault,
			Kind:      kind,
		})
	}
	return devices, nil
}

// Resolve turns a config value into a device.
//
// It accepts an empty string (meaning the system default), a device index, or
// any part of the device name -- so config can say "Headphones" and keep
// working after Windows renumbers its devices, which it does on every
// reconnect.
func Resolve(spec string, kind Kind) (*Device, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, nil // the caller falls back to the system default
	}

	devices, err := List(kind)
	if err != nil {
		return nil, err
	}
	if len(devices) == 0 {
		return nil, fmt.Errorf("%w: no %s devices", ErrNoDevice, kind)
	}

	// An endpoint id is exact, and is what the settings app stores.
	for index := range devices {
		if devices[index].ID == spec {
			return &devices[index], nil
		}
	}

	if index, err := strconv.Atoi(spec); err == nil {
		for position := range devices {
			if devices[position].Index == index {
				return &devices[position], nil
			}
		}
		return nil, nil // fall back to the default rather than failing
	}

	lowered := strings.ToLower(spec)
	for index := range devices {
		if strings.ToLower(devices[index].Name) == lowered {
			return &devices[index], nil
		}
	}
	for index := range devices {
		if strings.Contains(strings.ToLower(devices[index].Name), lowered) {
			return &devices[index], nil
		}
	}

	// Last resort: the name in config may be an old one, or itself a
	// mishearing, so accept a close match rather than going silent.
	names := make([]string, len(devices))
	for index, device := range devices {
		names[index] = strings.ToLower(device.Name)
	}
	if index, score := fuzzy.ExtractOne(lowered, names, fuzzy.PartialRatio); score >= 80 {
		return &devices[index], nil
	}
	return nil, nil
}

// Default is the device Windows would pick.
func Default(kind Kind) (*Device, error) {
	devices, err := List(kind)
	if err != nil {
		return nil, err
	}
	for index := range devices {
		if devices[index].IsDefault {
			return &devices[index], nil
		}
	}
	if len(devices) > 0 {
		return &devices[0], nil
	}
	return nil, fmt.Errorf("%w: no %s devices", ErrNoDevice, kind)
}

// Describe is the human-readable list the setup wizard reads aloud.
func Describe(kind Kind) []string {
	devices, err := List(kind)
	if err != nil {
		return nil
	}
	lines := make([]string, 0, len(devices))
	for index, device := range devices {
		lines = append(lines, fmt.Sprintf("%d. %s", index+1, device.Label()))
	}
	return lines
}

// Tone renders a short sine tone with fades on both ends, so it reads as a
// chime rather than as a click.
func Tone(frequency, duration float64, sampleRate int, volume float64) []float32 {
	samples := int(float64(sampleRate) * duration)
	if samples <= 0 {
		return nil
	}
	wave := make([]float32, samples)
	step := 2.0 * math.Pi * frequency / float64(sampleRate)
	for index := range wave {
		wave[index] = float32(math.Sin(step*float64(index)) * volume)
	}

	fade := int(float64(sampleRate) * 0.012)
	if fade < 1 {
		fade = 1
	}
	if samples > 2*fade {
		for index := 0; index < fade; index++ {
			ramp := float32(index) / float32(fade)
			wave[index] *= ramp
			wave[samples-1-index] *= ramp
		}
	}
	return wave
}
