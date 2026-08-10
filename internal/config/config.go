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

// FormatterConfig maps file extensions to an external formatter command.
// Command may be a bare name (looked up in PATH) or a full/tilde path.
// Use {file} in Args as a placeholder for the file path (e.g. prettier needs it).
type FormatterConfig struct {
	Extensions []string `toml:"extensions"`
	Command    string   `toml:"command"`
	Args       []string `toml:"args,omitempty"`
}

// Keybind overrides or adds a single key binding. Mode selects which mode's
// bindings to affect ("normal" or "insert"); Action must name one of the
// editor's built-in actions (see docs/configuration.md for the full list —
// e.g. "save", "cursor-left", "delete-selection"). A Key already bound to a
// multi-key prefix menu (like "g" or "m") can't be overridden this way.
type Keybind struct {
	Mode   string `toml:"mode"`
	Key    string `toml:"key"`
	Action string `toml:"action"`
}

// LinterConfig maps file extensions to an external linter command whose
// output is parsed into diagnostics alongside whatever the file's LSP server
// already reports. Command may be a bare name (looked up in PATH) or a
// full/tilde path. Use {file} in Args as a placeholder for the file path.
// Format selects which output parser to use (see internal/lint) — it must
// match one of that package's registered parsers (e.g. "golangci-lint-json").
// Stdin marks a linter that reads source from stdin (with {file} passed
// separately, e.g. via --stdin-filename, purely for config/parser
// resolution) rather than from disk — this is what lets it re-lint the
// buffer's live, unsaved content on every edit instead of only on save.
// Linters without an in-memory/stdin mode (golangci-lint, cargo clippy —
// both need a real on-disk project to compile against) stay save-triggered.
type LinterConfig struct {
	Extensions []string `toml:"extensions"`
	Command    string   `toml:"command"`
	Args       []string `toml:"args,omitempty"`
	Format     string   `toml:"format"`
	Stdin      bool     `toml:"stdin,omitempty"`
}

// DefaultLinters are tried when no user linter config matches an extension.
// Only entries whose command is found in PATH (or node_modules/.bin) are used.
var DefaultLinters = []LinterConfig{
	{Extensions: []string{"go"}, Command: "golangci-lint",
		Args: []string{"run", "--output.json.path=stdout", "{file}"}, Format: "golangci-lint-json"},
	{Extensions: []string{"js", "jsx", "ts", "tsx"}, Command: "eslint",
		Args:   []string{"--stdin", "--stdin-filename", "{file}", "--format", "json"},
		Format: "eslint-json", Stdin: true},
	{Extensions: []string{"py"}, Command: "ruff",
		Args:   []string{"check", "--stdin-filename", "{file}", "--output-format", "json", "-"},
		Format: "ruff-json", Stdin: true},
	// cargo clippy has no single-file argument — it lints the whole crate
	// containing the saved file, discovered from Manager's workDir.
	{Extensions: []string{"rs"}, Command: "cargo",
		Args: []string{"clippy", "--message-format", "json"}, Format: "cargo-clippy-json"},
}

// DefaultFormatters are tried (in order) when no user formatter config matches
// an extension. Only entries whose command is found in PATH are used.
// {file} in Args is replaced with the actual file path at runtime.
var DefaultFormatters = []FormatterConfig{
	{Extensions: []string{"go"}, Command: "gofmt"},
	{Extensions: []string{"rs"}, Command: "rustfmt", Args: []string{"--edition", "2021"}},
	{Extensions: []string{"py"}, Command: "black", Args: []string{"-q", "-"}},
	{Extensions: []string{"js", "jsx", "ts", "tsx", "css", "html", "json", "yaml", "yml", "md"},
		Command: "prettier", Args: []string{"--stdin-filepath", "{file}"}},
	{Extensions: []string{"c", "cpp", "h", "hpp"}, Command: "clang-format"},
	{Extensions: []string{"sh", "bash"}, Command: "shfmt"},
	{Extensions: []string{"lua"}, Command: "stylua", Args: []string{"-"}},
	{Extensions: []string{"zig"}, Command: "zig", Args: []string{"fmt", "--stdin"}},
	{Extensions: []string{"nix"}, Command: "nixpkgs-fmt"},
	{Extensions: []string{"toml"}, Command: "taplo", Args: []string{"fmt", "-"}},
	{Extensions: []string{"swift"}, Command: "swiftformat", Args: []string{"--stdinpath", "{file}"}},
	{Extensions: []string{"rb"}, Command: "rubocop",
		Args: []string{"--stdin", "{file}", "-a", "--format", "quiet"}},
	{Extensions: []string{"java"}, Command: "google-java-format", Args: []string{"-"}},
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

// IndentSettings controls the whitespace used for one indent level.
// Style is "tabs" or "spaces"; Width is the number of spaces per level
// (also used as the display width when Style is "tabs"). Either field may
// be left zero-valued to inherit from a less specific source — see
// Config.EffectiveIndent.
type IndentSettings struct {
	Style string `toml:"style"`
	Width int    `toml:"width"`
}

// defaultIndentSettings are indigo's built-in per-language conventions,
// used when the user hasn't set indent_style/indent_width (globally or for
// that language). Keyed the same way as DefaultFormatters/
// defaultLanguageServers: a bare extension, no leading dot.
var defaultIndentSettings = map[string]IndentSettings{
	"go":    {Style: "tabs", Width: 4},
	"rs":    {Style: "spaces", Width: 4},
	"py":    {Style: "spaces", Width: 4},
	"rb":    {Style: "spaces", Width: 2},
	"js":    {Style: "spaces", Width: 2},
	"jsx":   {Style: "spaces", Width: 2},
	"ts":    {Style: "spaces", Width: 2},
	"tsx":   {Style: "spaces", Width: 2},
	"json":  {Style: "spaces", Width: 2},
	"jsonc": {Style: "spaces", Width: 2},
	"yaml":  {Style: "spaces", Width: 2},
	"yml":   {Style: "spaces", Width: 2},
	"html":  {Style: "spaces", Width: 2},
	"css":   {Style: "spaces", Width: 2},
	"c":     {Style: "spaces", Width: 4},
	"cpp":   {Style: "spaces", Width: 4},
	"h":     {Style: "spaces", Width: 4},
	"hpp":   {Style: "spaces", Width: 4},
	"java":  {Style: "spaces", Width: 4},
	"lua":   {Style: "spaces", Width: 2},
	"zig":   {Style: "spaces", Width: 4},
	"nix":   {Style: "spaces", Width: 2},
	"toml":  {Style: "spaces", Width: 2},
	"sh":    {Style: "spaces", Width: 2},
	"bash":  {Style: "spaces", Width: 2},
	"swift": {Style: "spaces", Width: 4},
}

// Config holds user preferences loaded from ~/.config/indigo/config.toml.
// Absent keys keep their default values.
type Config struct {
	LineNumbers          bool              `toml:"line_numbers"`
	RecoveryMaxBytes     int64             `toml:"recovery_max_bytes"`
	RecoveryIntervalSecs int               `toml:"recovery_interval_secs"`
	LanguageServers      []LanguageServer  `toml:"language_server"`
	HideTabs             bool              `toml:"hide_tabs"`
	FuzzySearch          bool              `toml:"fuzzy_search"`
	FormatOnSave         bool              `toml:"format_on_save"`
	Formatters           []FormatterConfig `toml:"formatter"`
	Theme                string            `toml:"theme"`
	BracketColors        bool              `toml:"bracket_colors"`
	IndentGuides         bool              `toml:"indent_guides"`
	InlayHints           bool              `toml:"inlay_hints"`
	SemanticTokens       bool              `toml:"semantic_tokens"`
	// FileTypes maps file extensions or filenames to a syntax language key.
	// Keys are extensions (with or without leading dot) or bare filenames.
	// Values are a registered language key such as "sh", "go", ".md", etc.
	// Example: {".env" = "sh", ".mdx" = "md"}
	FileTypes map[string]string `toml:"file_types"`
	// IndentStyle and IndentWidth set the global default indent used for
	// languages with no built-in or user-configured per-language setting.
	// IndentStyle is "tabs" or "spaces"; empty/zero mean "use indigo's
	// built-in default (tabs, width 4)".
	IndentStyle string `toml:"indent_style"`
	IndentWidth int    `toml:"indent_width"`
	// IndentOverrides sets per-language indent settings, keyed by bare
	// extension (e.g. "py", "go"). Takes precedence over both
	// IndentStyle/IndentWidth and indigo's built-in per-language defaults.
	// A partial entry (only style or only width) inherits the other field
	// from the next source down the precedence chain.
	IndentOverrides map[string]IndentSettings `toml:"indent"`
	// Keybinds override or add Normal-/Insert-mode key bindings. See the
	// Keybind type for the shape of each entry.
	Keybinds []Keybind `toml:"keybind"`
	// Linters override or add an external linter for a given set of file
	// extensions, merged alongside the file's LSP-reported diagnostics.
	Linters []LinterConfig `toml:"linter"`
	// PickerIgnoreDirs names additional directories (matched by base name,
	// anywhere in the tree) to hide from the file picker, recent files, and
	// workspace grep — on top of the built-in defaults (.git, node_modules,
	// vendor, etc.). Example: ["build", "dist"].
	PickerIgnoreDirs []string `toml:"picker_ignore_dirs"`
}

func defaults() *Config {
	return &Config{
		LineNumbers:          true,
		RecoveryMaxBytes:     100 * 1024 * 1024,
		RecoveryIntervalSecs: 5,
		FuzzySearch:          true,
		BracketColors:        true,
		IndentGuides:         true,
		InlayHints:           true,
		SemanticTokens:       false,
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

// EffectiveFormatters returns user-configured formatters merged with built-in
// defaults. User entries take precedence: if a user entry covers an extension,
// the default for that extension is not included.
func (c *Config) EffectiveFormatters() []FormatterConfig {
	covered := make(map[string]bool)
	result := make([]FormatterConfig, 0, len(c.Formatters)+len(DefaultFormatters))
	for _, f := range c.Formatters {
		result = append(result, f)
		for _, ext := range f.Extensions {
			covered[ext] = true
		}
	}
	for _, f := range DefaultFormatters {
		shadowed := false
		for _, ext := range f.Extensions {
			if covered[ext] {
				shadowed = true
				break
			}
		}
		if !shadowed {
			result = append(result, f)
		}
	}
	return result
}

// EffectiveIndent returns the indent settings to use for a file with the
// given extension (bare, no leading dot — e.g. "py", "go"). Precedence,
// most specific first: a user IndentOverrides entry for ext, the user's
// global IndentStyle/IndentWidth, indigo's built-in per-language default,
// then a hardcoded tabs/width-4 fallback. A partial override (only Style or
// only Width set) inherits the other field from the next source down this
// chain rather than resetting it.
func (c *Config) EffectiveIndent(ext string) IndentSettings {
	result := IndentSettings{Style: "tabs", Width: 4}
	if def, ok := defaultIndentSettings[ext]; ok {
		result = def
	}
	if c.IndentStyle != "" {
		result.Style = c.IndentStyle
	}
	if c.IndentWidth != 0 {
		result.Width = c.IndentWidth
	}
	if user, ok := c.IndentOverrides[ext]; ok {
		if user.Style != "" {
			result.Style = user.Style
		}
		if user.Width != 0 {
			result.Width = user.Width
		}
	}
	return result
}

// ConfigDir returns the XDG config home: $XDG_CONFIG_HOME if set, else ~/.config.
func ConfigDir() (string, error) {
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return d, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config"), nil
}

// defaultConfigTemplate is written to disk the first time indigo runs.
// Every option is commented out so the file acts as self-documenting
// reference without changing any behaviour.
const defaultConfigTemplate = `# Indigo editor configuration
#
# This file was created automatically with all settings at their default values.
# Every option is commented out. To change a setting, remove the leading '#'
# and update the value.

# Show line numbers in the left gutter.
# line_numbers = true

# Maximum file size in bytes for which crash-recovery snapshots are kept.
# Set to 0 to disable recovery entirely.
# recovery_max_bytes = 104857600   # 100 MiB

# How often recovery snapshots are written to disk, in seconds.
# recovery_interval_secs = 5

# Hide the visible tab markers when multiple files are open.
# hide_tabs = false

# Use fuzzy matching in the file picker (as opposed to prefix matching).
# fuzzy_search = true

# Run the configured formatter automatically whenever a file is saved.
# format_on_save = false

# Color theme. Built-in options: default-dark, dracula, catppuccin-mocha, gruvbox-dark, one-dark.
# Custom themes go in ~/.config/indigo/themes/<name>.toml.
# theme = "default-dark"

# Colorize matching bracket pairs with cycling colors based on nesting depth.
# bracket_colors = true

# Draw indent guide lines at each tab-stop in the leading whitespace of lines.
# indent_guides = true

# Default indent style/width for languages with no built-in or per-language
# setting below. style is "tabs" or "spaces". Indigo also auto-detects the
# style already used in a file you open and prefers that over any of this.
# indent_style = "tabs"
# indent_width = 4

# ---------------------------------------------------------------------------
# Per-language indent overrides
#
# Indigo has built-in per-language conventions (e.g. tabs for Go, 4-space
# for Python, 2-space for JS/TS/JSON/YAML/CSS/HTML). Add an [indent.<ext>]
# block to override the style and/or width for a given extension.
#
# [indent.py]
# style = "spaces"
# width = 4
# ---------------------------------------------------------------------------

# ---------------------------------------------------------------------------
# Language servers
#
# Indigo has built-in defaults for Go, Rust, TypeScript/JavaScript, Python,
# C/C++, Lua, Ruby, Java, and Zig. Add a [[language_server]] block to
# override or add a server for a given set of file extensions.
#
# [[language_server]]
# extensions = ["go"]
# command    = "gopls"
# args       = []          # optional extra arguments
#
# [[language_server]]
# extensions = ["rs"]
# command    = "rust-analyzer"
# ---------------------------------------------------------------------------

# ---------------------------------------------------------------------------
# File type aliases
#
# Map file extensions or filenames to a syntax-highlighting language.
# The key is the extension (with or without leading dot) or a bare filename.
# The value is a recognized language key such as "sh", "go", "md", etc.
# Built-in aliases already defined: .env→sh, .envrc→sh, .mdx→md, .jsonc→json.
#
# [file_types]
# ".myext" = "go"
# "Jenkinsfile" = "sh"
# ---------------------------------------------------------------------------

# ---------------------------------------------------------------------------
# Formatters
#
# Indigo has built-in formatter defaults (gofmt, rustfmt, prettier, etc.).
# Add a [[formatter]] block to override or add a formatter for a given set
# of file extensions.
#
# [[formatter]]
# extensions = ["go"]
# command    = "gofmt"
# args       = []          # optional; use {file} as a placeholder for the path
#
# [[formatter]]
# extensions = ["js", "ts"]
# command    = "prettier"
# args       = ["--stdin-filepath", "{file}"]
# ---------------------------------------------------------------------------

# ---------------------------------------------------------------------------
# Linters
#
# Indigo has a built-in default for Go (golangci-lint). Add a [[linter]]
# block to override or add a linter for a given set of file extensions.
# Results are merged with the file's LSP-reported diagnostics. format must
# name one of internal/lint's registered output parsers.
#
# [[linter]]
# extensions = ["go"]
# command    = "golangci-lint"
# args       = ["run", "--output.json.path=stdout", "{file}"]
# format     = "golangci-lint-json"
# ---------------------------------------------------------------------------

# ---------------------------------------------------------------------------
# Key bindings
#
# Add a [[keybind]] block to rebind an existing action to a different key, or
# to bind an additional key to it. mode is "normal" or "insert"; action must
# be one of the editor's built-in action names (see docs/configuration.md).
# A key that's already a multi-key prefix menu (e.g. "g", "m") can't be
# overridden this way.
#
# [[keybind]]
# mode   = "normal"
# key    = "ctrl+p"
# action = "open-file-picker"
# ---------------------------------------------------------------------------
`

// Path returns the absolute path of the user config file.
// The file may not exist yet.
func Path() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "indigo", "config.toml"), nil
}

// Load reads the config file, returning defaults for any missing or
// unreadable file. A parse error is the only failure that is returned.
// When the file does not exist, a commented-out template is created so the
// user has a ready-made reference to all available settings.
func Load() (*Config, error) {
	cfg := defaults()

	dir, err := ConfigDir()
	if err != nil {
		return cfg, nil
	}

	indigoDir := filepath.Join(dir, "indigo")
	path := filepath.Join(indigoDir, "config.toml")
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			// Best-effort: create the directory and write the template.
			// Failures here are silently ignored so a read-only home
			// directory doesn't prevent the editor from starting.
			if mkErr := os.MkdirAll(indigoDir, 0o755); mkErr == nil {
				_ = os.WriteFile(path, []byte(defaultConfigTemplate), 0o644)
			}
			return cfg, nil
		}
		return cfg, err
	}
	defer f.Close() //nolint:errcheck

	if _, err := toml.NewDecoder(f).Decode(cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}
