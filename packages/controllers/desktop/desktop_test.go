package desktop

import (
	"reflect"
	"testing"
)

// The decisions here are the ones that matter when nobody is looking at the
// screen: what must never be closed, and what she is told is left open.

func TestMikkiLensAndTheDesktopAreNeverClosed(t *testing.T) {
	for _, name := range []string{
		"MikkiLens.exe", "mikkilensd.exe", // the voice that was told to do this
		"explorer.exe", "TextInputHost.exe", "StartMenuExperienceHost.exe",
	} {
		if !Protected(name) {
			t.Errorf("%q is not protected; closing it would take the desktop "+
				"or MikkiLens itself down mid-command", name)
		}
	}
	for _, name := range []string{"obs64.exe", "chrome.exe", "Code.exe", ""} {
		if Protected(name) {
			t.Errorf("%q is protected, so it would never be closed", name)
		}
	}
}

func TestClosableLeavesOutTheDesktopAndTitlelessWindows(t *testing.T) {
	windows := []Window{
		{Handle: 1, Title: "OBS 30.0.2", Name: "obs64.exe"},
		{Handle: 2, Title: "Program Manager", Name: "explorer.exe"},
		{Handle: 3, Title: "", Name: "chrome.exe"}, // a window with no title is not one she opened
		{Handle: 4, Title: "MikkiLens", Name: "MikkiLens.exe"},
		{Handle: 5, Title: "stream notes - Notepad", Name: "notepad.exe"},
	}

	got := Closable(windows)
	if len(got) != 2 {
		t.Fatalf("closable = %v, want OBS and Notepad", got)
	}
	if got[0].Name != "obs64.exe" || got[1].Name != "notepad.exe" {
		t.Errorf("closable = %v, want OBS and Notepad in that order", got)
	}
}

// Four browser windows are one thing she has to deal with, and "Chrome,
// Chrome, Chrome and Chrome" is not a list.
func TestNamesAreOnePerApplication(t *testing.T) {
	windows := []Window{
		{Title: "one", Name: "chrome.exe"},
		{Title: "two", Name: "Chrome.exe"},
		{Title: "three", Name: "obs64.exe"},
		{Title: "four", Name: "chrome.exe"},
	}

	if got, want := Names(windows), []string{"chrome", "obs64"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Names() = %v, want %v", got, want)
	}
}

func TestNamesOfNothingIsNothing(t *testing.T) {
	if got := Names(nil); len(got) != 0 {
		t.Errorf("Names(nil) = %v, want nothing", got)
	}
}
