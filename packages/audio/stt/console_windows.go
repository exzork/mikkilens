//go:build windows

package stt

import (
	"os/exec"
	"syscall"
)

// hideConsole keeps the recognizer's console window from flashing up on every
// command. She cannot see it, but it steals focus from whatever she is doing,
// and on a stream the viewers see it too.
func hideConsole(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}
