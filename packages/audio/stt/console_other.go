//go:build !windows

package stt

import "os/exec"

// hideConsole has nothing to hide away from Windows.
func hideConsole(*exec.Cmd) {}
