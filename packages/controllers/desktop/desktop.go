// Package desktop closes the applications on her screen, politely.
//
// "Tutup semua" at the end of a stream is a dozen windows to find and close,
// one at a time, on a machine she is not looking at. It is the last thing
// standing between finishing a stream and standing up, and it is exactly the
// kind of work a voice command should take.
//
// Every window is asked to close rather than killed. The difference matters:
// an editor with unsaved work puts its own dialog up and waits, where killing
// it would throw the work away. So this can leave windows open, and saying
// which ones is part of the job -- "everything closed except OBS" is a
// different thing to hear than silence.
package desktop

import (
	"sort"
	"strings"
)

// Window is one application window on the screen.
type Window struct {
	// Handle identifies the window to the operating system.
	Handle uintptr

	// Title is what the window calls itself, which is what she would
	// recognise it by.
	Title string

	// Process is the id it belongs to, and Name the executable's file name in
	// lower case: "obs64.exe".
	Process uint32
	Name    string
}

// protected are the processes never asked to close.
//
// MikkiLens itself is the obvious one: closing the thing that was told to
// close everything would end the command halfway through, with no voice left
// to say what happened. The rest are the desktop -- the shell, the task bar,
// the input host -- which are not applications she opened and which Windows
// puts back anyway.
var protected = map[string]bool{
	"mikkilens.exe":               true,
	"mikkilensd.exe":              true,
	"explorer.exe":                true,
	"searchhost.exe":              true,
	"startmenuexperiencehost.exe": true,
	"shellexperiencehost.exe":     true,
	"textinputhost.exe":           true,
	"applicationframehost.exe":    true,
	"systemsettings.exe":          true,
	"lockapp.exe":                 true,
	"dwm.exe":                     true,
}

// Protected reports whether a process is one that must be left alone.
func Protected(name string) bool { return protected[strings.ToLower(strings.TrimSpace(name))] }

// Closable is the windows worth asking to close: the ones a person would call
// an open application, with the desktop and MikkiLens itself left out.
func Closable(windows []Window) []Window {
	var found []Window
	for _, window := range windows {
		if Protected(window.Name) || strings.TrimSpace(window.Title) == "" {
			continue
		}
		found = append(found, window)
	}
	return found
}

// Names is what is left, as a sentence: the applications by name, each one
// once, in a stable order.
//
// By process rather than by window, because four browser windows are one thing
// she has to deal with and "Chrome, Chrome, Chrome and Chrome" is not a list.
func Names(windows []Window) []string {
	seen := map[string]bool{}
	var names []string
	for _, window := range windows {
		name := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(window.Name)), ".exe")
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
