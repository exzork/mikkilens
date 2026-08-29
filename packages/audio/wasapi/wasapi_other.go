//go:build !windows

// Package wasapi binds the Windows audio stack. MikkiLens drives OBS on a
// Windows streaming machine, and audio is the one layer with no portable
// equivalent worth pretending to.
//
// Everywhere else this reports that plainly rather than silently doing
// nothing: silence that looks like success is the failure mode this whole
// application is built to avoid.
package wasapi

import "errors"

// Direction is which half of the sound card we mean.
type Direction int

const (
	Render  Direction = 0
	Capture Direction = 1
)

// Endpoint is one audio device.
type Endpoint struct {
	ID        string
	Name      string
	IsDefault bool
	Direction Direction
}

// Interrupt cuts a sound short from another goroutine.
type Interrupt interface{ Stopped() bool }

// OnAudio receives interleaved float32 frames as they arrive.
type OnAudio func(samples []float32)

// Recorder is an open microphone stream.
type Recorder struct{}

var errUnsupported = errors.New("audio is only available on Windows")

func Endpoints(Direction) ([]Endpoint, error) { return nil, errUnsupported }

func Play(string, []float32, int, int, Interrupt) (bool, error) { return false, errUnsupported }

func StartCapture(string, int, int, OnAudio) (*Recorder, error) { return nil, errUnsupported }

func (r *Recorder) Stop()            {}
func (r *Recorder) Running() bool    { return false }
func (r *Recorder) LastError() error { return errUnsupported }
