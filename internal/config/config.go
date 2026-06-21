package config

import (
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// Config holds user preferences loaded from ~/.config/twist/config.toml.
// Absent keys keep their default values.
type Config struct {
	LineNumbers bool `toml:"line_numbers"`
}

func defaults() *Config {
	return &Config{
		LineNumbers: true,
	}
}

// Load reads the config file, returning defaults for any missing or
// unreadable file. A parse error is the only failure that is returned.
func Load() (*Config, error) {
	cfg := defaults()

	dir, err := os.UserConfigDir()
	if err != nil {
		return cfg, nil
	}

	path := filepath.Join(dir, "twist", "config.toml")
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
