//go:build windows

package llm

import (
	"os/exec"
	"syscall"
)

// hideConsole keeps the model server from opening a console window.
//
// Without it a black window appears over whatever she has in front of her
// every time the server starts -- and worse, over the scene she is streaming.
func hideConsole(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}
