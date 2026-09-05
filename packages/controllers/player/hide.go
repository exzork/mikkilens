package player

import (
	"os/exec"
	"syscall"
)

// hideWindow keeps a console from flashing up when a helper program starts.
//
// yt-dlp and ffmpeg are console programs, and starting one from a windowed
// application pops a black box onto the screen for as long as it runs. On a
// machine that is capturing that screen, that is a black box on the stream --
// once to resolve the song, and again for the whole length of it.
//
// Windows only, like the rest of this application: the audio capture is
// WASAPI and the hotkeys are RegisterHotKey, so there is nothing here that
// would run anywhere else even if this compiled.
func hideWindow(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}
