package config

import (
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// LanguageServer maps file extensions to a language server command.
type LanguageServer struct {
	Extensions []string `toml:"extensions"`
	Command    string   `toml:"command"`
	Args       []string `toml:"args,omitempty"`
}

// defaultLanguageServers are used when the user has not configured a server
// for a given extension. First match wins.
var defaultLanguageServers = []LanguageServer{
	{Extensions: []string{"go"}, Command: "gopls"},
	{Extensions: []string{"rs"}, Command: "rust-analyzer"},
	{Extensions: []string{"ts", "tsx"}, Command: "typescript-language-server", Args: []string{"--stdio"}},
	{Extensions: []string{"js", "jsx"}, Command: "typescript-language-server", Args: []string{"--stdio"}},
	{Extensions: []string{"py"}, Command: "pylsp"},
	{Extensions: []string{"c", "cpp", "h", "hpp"}, Command: "clangd"},
	{Extensions: []string{"lua"}, Command: "lua-language-server"},
	{Extensions: []string{"rb"}, Command: "solargraph", Args: []string{"stdio"}},
	{Extensions: []string{"java"}, Command: "jdtls"},
	{Extensions: []string{"zig"}, Command: "zls"},
}

// Config holds user preferences loaded from ~/.config/indigo/config.toml.
// Absent keys keep their default values.
type Config struct {
	LineNumbers          bool             `toml:"line_numbers"`
	RecoveryMaxBytes     int64            `toml:"recovery_max_bytes"`
	RecoveryIntervalSecs int              `toml:"recovery_interval_secs"`
	LanguageServers      []LanguageServer `toml:"language_server"`
}

func defaults() *Config {
	return &Config{
		LineNumbers:          true,
		RecoveryMaxBytes:     100 * 1024 * 1024,
		RecoveryIntervalSecs: 5,
	}
}

// EffectiveLanguageServers returns user-configured servers merged with built-in
// defaults. User entries take precedence: if a user entry covers an extension,
// the default for that extension is not included.
func (c *Config) EffectiveLanguageServers() []LanguageServer {
	covered := make(map[string]bool)
	result := make([]LanguageServer, 0, len(c.LanguageServers)+len(defaultLanguageServers))
	for _, ls := range c.LanguageServers {
		result = append(result, ls)
		for _, ext := range ls.Extensions {
			covered[ext] = true
		}
	}
	for _, ls := range defaultLanguageServers {
		shadowed := false
		for _, ext := range ls.Extensions {
			if covered[ext] {
				shadowed = true
				break
			}
		}
		if !shadowed {
			result = append(result, ls)
		}
	}
	return result
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
