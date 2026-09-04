//go:build !windows

package httpapi

import (
	"errors"
	"os/exec"
	"runtime"

	"github.com/exzork/mikkilens/packages/controllers/youtube"
)

// Running at login is a Windows Startup-folder shortcut, and MikkiLens targets
// a Windows streaming machine. Elsewhere the setting reports that plainly
// rather than pretending to have taken effect.

func StartupEnabled() bool { return false }

func SetStartup(bool) error {
	return errors.New("running at login is only available on Windows")
}

// OpenBrowser opens a URL in the desktop's browser.
func OpenBrowser(url string) {
	command := "xdg-open"
	if runtime.GOOS == "darwin" {
		command = "open"
	}
	_ = exec.Command(command, url).Start()
}
