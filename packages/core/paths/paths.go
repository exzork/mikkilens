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

func ConfigFile() string    { return join("config.toml") }
func ConfigExample() string { return join("config.example.toml") }
func LocalesDir() string    { return join("locales") }
func LogFile() string       { return filepath.Join(DataDir(), "mikkilens.log") }
func SecretsFile() string   { return filepath.Join(DataDir(), "secrets.toml") }

// TokenFile is the cached YouTube sign-in; ClientSecretFile is the OAuth
// client it was issued by. Both are hers, both live in data, and neither is
// ever committed.
func TokenFile() string        { return filepath.Join(DataDir(), "youtube_token.json") }
func ClientSecretFile() string { return filepath.Join(DataDir(), "client_secret.json") }

// AccountsDir holds one sign-in per YouTube channel she runs.
//
// TokenFile above is the single sign-in this replaced, and it stays: an
// installation that has one is migrated into an account here the first time the
// channel it belongs to can be named. Deleting it on sight would have signed
// her out of the only channel she had, over a version upgrade she did not ask
// for, to fix a problem she does not have yet.
func AccountsDir() string { return filepath.Join(DataDir(), "youtube") }

// EnsureAccountsDir creates the accounts directory if it is not there yet.
func EnsureAccountsDir() (string, error) {
	dir := AccountsDir()
	return dir, os.MkdirAll(dir, 0o700)
}

// AccountFile is one channel's sign-in, named by its YouTube channel id.
//
// The id rather than a name she chose, because the name is hers to change and
// the file must not move when she does -- and because two channels can be given
// the same name by mistake, where two ids cannot collide.
func AccountFile(channelID string) string {
	return filepath.Join(AccountsDir(), safeFileName(channelID)+".json")
}

// safeFileName keeps a channel id from being read as a path. YouTube ids are
// letters, digits, dashes and underscores, so in practice nothing is replaced;
// this is here so that a malformed id becomes a useless filename rather than a
// write somewhere else.
func safeFileName(id string) string {
	cleaned := make([]rune, 0, len(id))
	for _, letter := range id {
		switch {
		case letter >= 'a' && letter <= 'z',
			letter >= 'A' && letter <= 'Z',
			letter >= '0' && letter <= '9',
			letter == '-', letter == '_':
			cleaned = append(cleaned, letter)
		default:
			cleaned = append(cleaned, '_')
		}
	}
	if len(cleaned) == 0 {
		return "account"
	}
	return string(cleaned)
}

func QuotaFile() string   { return filepath.Join(DataDir(), "quota.json") }
func TTSCacheDir() string { return filepath.Join(DataDir(), "tts_cache") }
func ModelsDir() string   { return filepath.Join(DataDir(), "models") }

// CommandsFile is her editable command file for one language.
func CommandsFile(language string) string {
	return join("commands." + language + ".toml")
}
