package obs

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func writeOBSConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// The shape is OBS's own, taken from a real install.
func TestReadsWhatOBSWroteDownForItself(t *testing.T) {
	path := writeOBSConfig(t, `{
		"server_enabled": true,
		"auth_required": true,
		"server_port": 4455,
		"server_password": "cal6iyf8zxBcwYf3"
	}`)

	server, ok := readLocalServer(path)
	if !ok {
		t.Fatal("the file was readable and did not read")
	}
	if !server.Enabled || !server.AuthRequired {
		t.Error("the switches did not come through")
	}
	if server.Port != 4455 || server.Password != "cal6iyf8zxBcwYf3" {
		t.Errorf("got port %d password %q", server.Port, server.Password)
	}
}

// Missing or unreadable is the ordinary case on a machine with no OBS, and
// must be quiet rather than an error: it only means there is nothing to fall
// back to.
func TestNoOBSConfigIsNotAFailure(t *testing.T) {
	if _, ok := readLocalServer(filepath.Join(t.TempDir(), "absent.json")); ok {
		t.Error("a missing file reported success")
	}
	if _, ok := readLocalServer(writeOBSConfig(t, "not json at all")); ok {
		t.Error("a corrupt file reported success")
	}
	if _, ok := readLocalServer(""); ok {
		t.Error("an empty path reported success")
	}
}

// Only this machine's OBS. Another machine's server has its own password and
// this one's is no business of ours.
func TestOnlyALocalOBSIsWorthReading(t *testing.T) {
	for _, host := range []string{"", "localhost", "127.0.0.1", "::1", "[::1]", "LOCALHOST"} {
		if !isLoopback(host) {
			t.Errorf("%q should count as this machine", host)
		}
	}
	for _, host := range []string{"192.168.1.20", "obs-pc", "example.com", "10.0.0.5"} {
		if isLoopback(host) {
			t.Errorf("%q should not count as this machine", host)
		}
	}

	// A remote host is asked with exactly what she configured and nothing else.
	if got := passwordsToTry("192.168.1.20", "typed"); !reflect.DeepEqual(got, []string{"typed"}) {
		t.Errorf("a remote OBS got %v, want only the configured password", got)
	}
}

// The order is the whole design: what she set is a decision and goes first,
// and OBS's own is the fallback for the two cases where hers cannot be right.
func TestTheConfiguredPasswordIsTriedFirst(t *testing.T) {
	if got := passwordsToTry("192.168.1.20", ""); !reflect.DeepEqual(got, []string{""}) {
		t.Errorf("got %v", got)
	}
}
