package config

import (
	"log/slog"
	"os"
	"sync"

	"github.com/pelletier/go-toml/v2"

	"github.com/exzork/mikkilens/packages/core/paths"
)

// Secrets live apart from config.toml so the config file stays safe to share
// when she needs someone to look at it. The environment wins over the file, so
// a key can be injected for one run without being written down anywhere.

var secretsMu sync.Mutex

// ResolveSecret looks a secret up: environment variable first, then
// data/secrets.toml.
func ResolveSecret(name string) string {
	if name == "" {
		return ""
	}
	if value := os.Getenv(name); value != "" {
		return value
	}
	return readSecrets()[name]
}

// StoreSecret persists a secret written through the settings page. An empty
// value removes it rather than storing a blank.
func StoreSecret(name, value string) error {
	if name == "" {
		return nil
	}
	secretsMu.Lock()
	defer secretsMu.Unlock()

	if _, err := paths.EnsureDataDir(); err != nil {
		return err
	}
	secrets := readSecretsLocked()
	if value == "" {
		delete(secrets, name)
	} else {
		secrets[name] = value
	}

	encoded, err := toml.Marshal(secrets)
	if err != nil {
		return err
	}
	return os.WriteFile(paths.SecretsFile(), encoded, 0o600)
}

func readSecrets() map[string]string {
	secretsMu.Lock()
	defer secretsMu.Unlock()
	return readSecretsLocked()
}

func readSecretsLocked() map[string]string {
	secrets := map[string]string{}
	data, err := os.ReadFile(paths.SecretsFile())
	if err != nil {
		return secrets
	}
	if err := toml.Unmarshal(data, &secrets); err != nil {
		slog.Error("data/secrets.toml is not valid TOML", "error", err)
		return map[string]string{}
	}
	return secrets
}
