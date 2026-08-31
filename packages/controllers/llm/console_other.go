//go:build !windows

package llm

import "os/exec"

func hideConsole(*exec.Cmd) {}
