// Package config loads Airtable credentials/settings from a JSON file in the
// user's config dir, with environment variables as overrides.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Config holds everything the Airtable client needs. json tags control how
// the struct maps to/from the config file's JSON keys.
type Config struct {
	PAT    string `json:"pat"`
	BaseID string `json:"baseId"`
	Table  string `json:"table"`

	// Accent overrides the UI's accent color. Empty (the default) means
	// "derive it from the terminal's own theme" -- a hex value like
	// "#FF6AC1" or a base-16 ANSI index like "5" both work, for when a
	// user's terminal theme doesn't make that distinction clearly (e.g. a
	// light/dark theme pair that reuses the same ANSI palette for both).
	Accent string `json:"accent,omitempty"`
}

// Path returns the on-disk location of the config file:
// ~/.config/airtable-tui/config.json
func Path() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "airtable-tui", "config.json"), nil
}

// Load reads the config file, then lets AIRTABLE_PAT / AIRTABLE_BASE /
// AIRTABLE_TABLE env vars override individual fields if set.
func Load() (Config, error) {
	var cfg Config

	path, err := Path()
	if err != nil {
		return cfg, err
	}

	// Missing file isn't fatal here -- env vars alone might be enough.
	data, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(data, &cfg); err != nil {
			return cfg, fmt.Errorf("parsing %s: %w", path, err)
		}
	} else if !os.IsNotExist(err) {
		return cfg, err
	}

	if v := os.Getenv("AIRTABLE_PAT"); v != "" {
		cfg.PAT = v
	}
	if v := os.Getenv("AIRTABLE_BASE"); v != "" {
		cfg.BaseID = v
	}
	if v := os.Getenv("AIRTABLE_TABLE"); v != "" {
		cfg.Table = v
	}
	if v := os.Getenv("AIRTABLE_ACCENT"); v != "" {
		cfg.Accent = v
	}

	if cfg.PAT == "" || cfg.BaseID == "" || cfg.Table == "" {
		return cfg, fmt.Errorf(
			"missing config: need pat, baseId, table (set in %s or via env vars)", path,
		)
	}

	return cfg, nil
}

// Save writes cfg to the config file, creating the directory if needed.
func Save(cfg Config) error {
	path, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
