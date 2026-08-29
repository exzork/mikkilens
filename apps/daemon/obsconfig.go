package main

import (
	"encoding/json"
	"fmt"
	"os"
)

// readOBSConfig loads OBS's own websocket settings file.
func readOBSConfig(path string) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("OBS WebSocket config not found at %s", path)
	}
	settings := map[string]any{}
	if err := json.Unmarshal(raw, &settings); err != nil {
		return nil, fmt.Errorf("could not read the OBS config: %w", err)
	}
	return settings, nil
}

// writeOBSConfig saves it back in the shape OBS wrote it.
func writeOBSConfig(path string, settings map[string]any) error {
	encoded, err := json.MarshalIndent(settings, "", "    ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, encoded, 0o644)
}
