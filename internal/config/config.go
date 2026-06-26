package config

import (
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// Config holds user preferences loaded from ~/.config/indigo/config.toml.
// Absent keys keep their default values.
type Config struct {
	LineNumbers          bool  `toml:"line_numbers"`
	RecoveryMaxBytes     int64 `toml:"recovery_max_bytes"`
	RecoveryIntervalSecs int   `toml:"recovery_interval_secs"`
}

func defaults() *Config {
	return &Config{
		LineNumbers:          true,
		RecoveryMaxBytes:     100 * 1024 * 1024, // 100 MiB
		RecoveryIntervalSecs: 5,
	}
}

// configDir returns the XDG config home: $XDG_CONFIG_HOME if set, else ~/.config.
func configDir() (string, error) {
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return d, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config"), nil
}

// Load reads the config file, returning defaults for any missing or
// unreadable file. A parse error is the only failure that is returned.
func Load() (*Config, error) {
	cfg := defaults()

	dir, err := configDir()
	if err != nil {
		return cfg, nil
	}

	path := filepath.Join(dir, "indigo", "config.toml")
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, err
	}
	defer f.Close()

	if _, err := toml.NewDecoder(f).Decode(cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}
