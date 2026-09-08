package obs

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
)

// OBS keeps its own WebSocket settings in a small JSON file next to the rest
// of its plugin config. It is the only place the generated password exists:
// the dialog shows it, and nothing else writes it down.
//
// os.UserConfigDir is the right root on all three platforms -- %AppData% on
// Windows, ~/Library/Application Support on macOS, ~/.config on Linux -- which
// is exactly where OBS puts obs-studio.
//
// LocalConfigPath is exported so the enable-obs command and this file cannot
// drift apart about where to look.
func LocalConfigPath() string {
	root, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(root, "obs-studio", "plugin_config", "obs-websocket", "config.json")
}

// localServer is what OBS records about its own server.
type localServer struct {
	Enabled      bool   `json:"server_enabled"`
	AuthRequired bool   `json:"auth_required"`
	Port         int    `json:"server_port"`
	Password     string `json:"server_password"`
}

// readLocalServer loads that file. Takes a path so it can be tested without a
// copy of OBS installed.
func readLocalServer(path string) (localServer, bool) {
	if path == "" {
		return localServer{}, false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return localServer{}, false
	}
	var found localServer
	if err := json.Unmarshal(raw, &found); err != nil {
		return localServer{}, false
	}
	return found, true
}

// isLoopback reports whether a host names the machine we are running on.
//
// This gates the whole idea. Reading the OBS installed here tells us nothing
// about an OBS on another machine, and quietly trying this machine's password
// against somebody else's server would be both useless and rude.
func isLoopback(host string) bool {
	host = strings.TrimSpace(strings.ToLower(host))
	host = strings.Trim(host, "[]")
	switch host {
	case "", "localhost":
		return true
	}
	if address := net.ParseIP(host); address != nil {
		return address.IsLoopback()
	}
	return false
}

// passwordsToTry is the configured password, then OBS's own.
//
// In that order, and only when they differ, because a password she typed is a
// decision and gets the first attempt. OBS's own is the fallback that covers
// the two cases where the typed one cannot be right: a fresh install that has
// never had one, and a password rotated in OBS since it was written down.
//
// Nothing is written back. Reading it live means a rotation simply works next
// time, where a copy saved into config.toml would go stale exactly the way the
// one it replaced did.
func passwordsToTry(host, configured string) []string {
	if !isLoopback(host) {
		return []string{configured}
	}
	server, ok := readLocalServer(LocalConfigPath())
	if !ok || server.Password == "" || server.Password == configured {
		return []string{configured}
	}
	if configured == "" {
		// Nothing to defer to, so there is no second attempt to make.
		return []string{server.Password}
	}
	return []string{configured, server.Password}
}
