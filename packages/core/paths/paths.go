// Package paths holds every canonical filesystem location. Everything else
// asks here rather than building its own path, so moving the data directory
// is one change in one file.
package paths

import (
	"os"
	"path/filepath"
	"sync"
)

var (
	mu   sync.RWMutex
	root string
)

// markers identify the project root when MIKKILENS_HOME is not set: any
// directory holding one of these is the installation she edits by hand.
var markers = []string{"config.toml", "commands.id.toml", "commands.en.toml", "go.mod"}

// SetRoot overrides the project root. Tests use it to keep writes contained.
func SetRoot(dir string) {
	mu.Lock()
	defer mu.Unlock()
	root = dir
}

// Root is the directory holding config.toml, the command files and data/.
//
// Resolution order: MIKKILENS_HOME, then the working directory or any parent
// carrying a marker file, then the directory the executable lives in. The
// executable comes last because `go test` and `go run` put the binary in a
// temporary directory that has nothing to do with the installation.
func Root() string {
	mu.RLock()
	if root != "" {
		defer mu.RUnlock()
		return root
	}
	mu.RUnlock()

	mu.Lock()
	defer mu.Unlock()
	if root != "" {
		return root
	}
	root = resolveRoot()
	return root
}

func resolveRoot() string {
	if home := os.Getenv("MIKKILENS_HOME"); home != "" {
		if abs, err := filepath.Abs(home); err == nil {
			return abs
		}
		return home
	}
	if cwd, err := os.Getwd(); err == nil {
		if found := walkUpForMarker(cwd); found != "" {
			return found
		}
	}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		if found := walkUpForMarker(dir); found != "" {
			return found
		}
		return dir
	}
	return "."
}

func walkUpForMarker(start string) string {
	dir := start
	for {
		for _, marker := range markers {
			if _, err := os.Stat(filepath.Join(dir, marker)); err == nil {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func join(parts ...string) string {
	return filepath.Join(append([]string{Root()}, parts...)...)
}

// DataDir holds everything MikkiLens writes: logs, tokens, caches, secrets.
func DataDir() string { return join("data") }

// EnsureDataDir creates the data directory if it is not there yet.
func EnsureDataDir() (string, error) {
	dir := DataDir()
	return dir, os.MkdirAll(dir, 0o755)
}

func ConfigFile() string       { return join("config.toml") }
func ConfigExample() string    { return join("config.example.toml") }
func LocalesDir() string       { return join("locales") }
func LogFile() string          { return filepath.Join(DataDir(), "mikkilens.log") }
func TokenFile() string        { return filepath.Join(DataDir(), "youtube_token.json") }
func ClientSecretFile() string { return filepath.Join(DataDir(), "client_secret.json") }
func SecretsFile() string      { return filepath.Join(DataDir(), "secrets.toml") }
func QuotaFile() string        { return filepath.Join(DataDir(), "quota.json") }
func TTSCacheDir() string      { return filepath.Join(DataDir(), "tts_cache") }
func ModelsDir() string        { return filepath.Join(DataDir(), "models") }

// CommandsFile is her editable command file for one language.
func CommandsFile(language string) string {
	return join("commands." + language + ".toml")
}
