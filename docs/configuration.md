# Configuration

Config file location: `~/.config/indigo/config.toml` (created automatically on first run if absent; all settings have defaults).

## General options

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `line_numbers` | bool | `true` | Show line numbers in the gutter |
| `hide_tabs` | bool | `false` | Hide the tab bar even when multiple buffers are open |
| `fuzzy_search` | bool | `true` | Use fuzzy matching in the file picker |
| `format_on_save` | bool | `false` | Run the file's formatter automatically on `:w` |
| `bracket_colors` | bool | `true` | Colorize matching bracket pairs with cycling colors based on nesting depth |
| `indent_guides` | bool | `true` | Draw indent-guide lines at each tab-stop in leading whitespace |
| `recovery_interval_secs` | int | `5` | How often (in seconds) unsaved content is written to the recovery directory |
| `recovery_max_bytes` | int | `104857600` | Maximum file size (bytes) eligible for crash recovery (default 100 MB); `0` disables recovery |
| `theme` | string | `"default-dark"` | Color theme name — see [Themes](#themes) below |

## Language servers

indigo starts language servers automatically when you open a file with a supported extension, as long as the server binary is in your PATH. No config is needed for the defaults.

### Built-in defaults

| Extensions | Server |
|-----------|--------|
| `.go` | `gopls` |
| `.rs` | `rust-analyzer` |
| `.ts` `.tsx` | `typescript-language-server --stdio` |
| `.js` `.jsx` | `typescript-language-server --stdio` |
| `.py` | `pylsp` |
| `.c` `.cpp` `.h` `.hpp` | `clangd` |
| `.lua` | `lua-language-server` |
| `.rb` | `solargraph stdio` |
| `.java` | `jdtls` |
| `.zig` | `zls` |

### Custom language servers

Add a `[[language_server]]` block for each override. User entries take precedence over built-in defaults for the listed extensions.

```toml
[[language_server]]
extensions = ["ts", "tsx"]
command    = "typescript-language-server"
args       = ["--stdio"]

[[language_server]]
extensions = ["ml", "mli"]
command    = "ocamllsp"
```

## Formatters

Formatters run on `:fmt` / `:format`, and automatically on `:w` when `format_on_save = true`.

### Built-in defaults

| Extensions | Formatter |
|-----------|-----------|
| `.go` | `gofmt` |
| `.rs` | `rustfmt --edition 2021` |
| `.py` | `black -q -` |
| `.js` `.jsx` `.ts` `.tsx` `.css` `.html` `.json` `.yaml` `.yml` `.md` | `prettier --stdin-filepath {file}` |
| `.c` `.cpp` `.h` `.hpp` | `clang-format` |
| `.sh` `.bash` | `shfmt` |
| `.lua` | `stylua -` |
| `.zig` | `zig fmt --stdin` |
| `.nix` | `nixpkgs-fmt` |
| `.toml` | `taplo fmt -` |
| `.swift` | `swiftformat --stdinpath {file}` |
| `.rb` | `rubocop --stdin {file} -a --format quiet` |
| `.java` | `google-java-format -` |

Only formatters whose command is found in PATH are used; missing tools are silently skipped.

### Custom formatters

Add a `[[formatter]]` block to override or extend the defaults. Use `{file}` as a placeholder for the file path when the formatter needs it (e.g. prettier).

```toml
[[formatter]]
extensions = ["js", "ts"]
command    = "biome"
args       = ["format", "--stdin-file-path", "{file}"]

[[formatter]]
extensions = ["py"]
command    = "ruff"
args       = ["format", "-"]
```

## File type aliases

Map a file extension or an exact filename to an existing syntax-highlighting language, for
files that don't use their language's usual extension. Built in: `.env`→sh, `.envrc`→sh,
`.mdx`→md, `.jsonc`→json.

```toml
[file_types]
".myext"      = "go"
"Jenkinsfile" = "sh"
```

Keys are matched as either a leading-dot extension or a bare filename; values are a
registered language key (`go`, `sh`, `md`, `json`, etc.).

## Themes

Set the active theme by name:

```toml
theme = "dracula"
```

### Built in

`default-dark` (the default), `dracula`, `catppuccin-mocha`, `gruvbox-dark`, `one-dark`.

### Custom themes

Drop a `<name>.toml` file in `~/.config/indigo/themes/` and set `theme = "<name>"`. User
themes take precedence over a built-in of the same name. A theme file has two sections:

```toml
name = "my-theme"

[ui]
bar_bg          = "#087AC8"   # status bar background
bar_fg          = "#FFFFFF"   # status bar text
bar_dark_bg     = "#065A96"   # file type / LSP segments in the status bar
normal_mode_fg  = "#AAFFAA"   # "NORMAL" mode label color
insert_mode_fg  = "#AADDFF"   # "INSERT" mode label color
selection_bg    = "#2D5F8A"
selection_fg    = "#FFFFFF"
gutter_fg       = "#606060"   # line numbers
gutter_cur_fg   = "#AAAAAA"   # line number of the current line
popup_bg        = "#1E2A38"   # file picker, menus, dialogs
popup_border_fg = "#4488CC"
popup_key_fg    = "#FFDD44"   # highlighted/selected item in a popup
popup_text_fg   = "#CCDDEE"
search_match_bg = "#444400"
search_match_fg = "#FFFF88"
search_cur_bg   = "#AAAA00"   # the currently-selected search match
search_cur_fg   = "#FFFFFF"
diag_error_fg   = "#FF5555"
diag_warn_fg    = "#FFDD44"
diag_info_fg    = "#88AAFF"
popup_border    = "rounded"   # rounded | square | double | none

[syntax]
"comment"          = { fg = "#6A9955" }
"string"           = { fg = "#CE9178" }
"keyword"          = { fg = "#C586C0" }
"function"         = { fg = "#DCDCAA" }
"type"             = { fg = "#4EC9B0" }
"variable"         = { fg = "#9CDCFE" }
"markup.strong"    = { fg = "#E5C07B", bold = true }
"markup.italic"    = { fg = "#D7BA7D", italic = true }
# ... see internal/theme/themes/default-dark.toml for the full set of scope keys
```

`[ui]` fields are all optional; any left out fall back to `rounded` for `popup_border` and
otherwise the zero value. `[syntax]` keys are tree-sitter highlight scopes (`comment`,
`string`, `keyword`, `function`, `type`, `variable`, `markup.*`, etc.) — a scope with no
entry just isn't colorized. Each entry supports `fg` (hex color), `bold`, and `italic`.

### Importing a theme from Helix or VSCode

```
indigo --import-theme helix:path/to/theme.toml
indigo --import-theme vscode:path/to/theme.json
```

This converts the given theme file to indigo's format and writes it to
`~/.config/indigo/themes/<name>.toml`, then prints the `theme = "<name>"` line to add to
`config.toml`.

## Plugins

Plugins are a separate mechanism from `config.toml` — installed binaries + manifests under
`~/.config/indigo/plugins/<name>/`, auto-discovered and started by the server (see
[plugin-architecture.md](plugin-architecture.md)). There is currently no config.toml-level
plugin configuration (enable/disable, per-plugin options); a plugin is "configured" simply
by being present in that directory.

## Full example

```toml
line_numbers   = true
hide_tabs      = false
fuzzy_search   = true
format_on_save = true
bracket_colors = true
indent_guides  = true
theme          = "dracula"

[file_types]
"Jenkinsfile" = "sh"

[[language_server]]
extensions = ["ml", "mli"]
command    = "ocamllsp"

[[formatter]]
extensions = ["js", "ts", "jsx", "tsx"]
command    = "biome"
args       = ["format", "--stdin-file-path", "{file}"]
```
