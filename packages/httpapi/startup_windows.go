//go:build windows

package httpapi

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// Running at login is a shortcut in the Startup folder rather than a registry
// entry: no COM, no elevation, and she or anyone helping her can see and
// delete it in Explorer.

func startupShortcut() string {
	return filepath.Join(os.Getenv("APPDATA"),
		"Microsoft", "Windows", "Start Menu", "Programs", "Startup", "MikkiLens.lnk")
}

// StartupEnabled reports whether MikkiLens runs when Windows starts.
func StartupEnabled() bool {
	_, err := os.Stat(startupShortcut())
	return err == nil
}

// SetStartup turns running at login on or off.
func SetStartup(enabled bool) error {
	shortcut := startupShortcut()
	if !enabled {
		if err := os.Remove(shortcut); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}

	if _, err := os.Stat(filepath.Dir(shortcut)); err != nil {
		return fmt.Errorf("could not find the Windows Startup folder: %w", err)
	}
	target, err := os.Executable()
	if err != nil {
		return err
	}

	// A shortcut is what Explorer expects here, and creating one is a COM call
	// PowerShell already wraps. Doing it this way keeps MikkiLens free of a COM
	// dependency for a feature used once.
	script := strings.NewReplacer(
		"%SHORTCUT%", escapePowerShell(shortcut),
		"%TARGET%", escapePowerShell(target),
		"%WORKDIR%", escapePowerShell(filepath.Dir(target)),
	).Replace(`
$shell = New-Object -ComObject WScript.Shell
$link = $shell.CreateShortcut('%SHORTCUT%')
$link.TargetPath = '%TARGET%'
$link.Arguments = 'run'
$link.WorkingDirectory = '%WORKDIR%'
$link.Description = 'MikkiLens voice control'
$link.WindowStyle = 7
$link.Save()
`)

	command := exec.Command("powershell",
		"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script)
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}

	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("could not create the startup shortcut: %s",
			strings.TrimSpace(string(output)))
	}
	return nil
}

func escapePowerShell(value string) string {
	return strings.ReplaceAll(value, "'", "''")
}

// OpenBrowser opens a URL in whatever browser Windows is set to use. It is how
// the OAuth consent screen reaches her.
func OpenBrowser(url string) {
	command := exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	_ = command.Start()
}
