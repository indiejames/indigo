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
| `inlay_hints` | bool | `true` | Show LSP inlay hints (parameter names, e.g. `foo(count: 5)`) as dim virtual text |
| `semantic_tokens` | bool | `false` | Recolor identifiers (variables, parameters, types, etc.) using LSP semantic tokens instead of syntax-only guesses |
| `recovery_interval_secs` | int | `5` | How often (in seconds) unsaved content is written to the recovery directory |
| `recovery_max_bytes` | int | `104857600` | Maximum file size (bytes) eligible for crash recovery (default 100 MB); `0` disables recovery |
| `theme` | string | `"default-dark"` | Color theme name — see [Themes](#themes) below |
| `cursor_column_style` | string | `"view"` | Status bar column number: `"view"` shows the on-screen visual column (tabs count for their visual width, VS Code-style); `"buffer"` shows the raw rune offset (a tab counts as one column, Helix-style) |

## File picker

The file picker (and workspace grep, and the recent-files list) always hides `.git`,
`vendor`, `node_modules`, `.svn`, `.hg`, `__pycache__`, and `.cache`. Add more directory
names — matched by base name, anywhere in the tree — with `picker_ignore_dirs`:

```toml
picker_ignore_dirs = ["build", "dist"]
```

There's no separate extension filter: when `fuzzy_search = true`, typing a query like `.go`
in the picker's search mode surfaces matching files first — paths whose name *ends with*
the query score well above ones that merely contain those characters elsewhere, so an extension
acts as a de facto filter without excluding anything outright. When `fuzzy_search = false`,
the picker uses simple substring matching with no ranking.

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

## Linters

Linter results are merged with whatever diagnostics the file's LSP server already reports,
so they show up the same way (gutter markers, the `D`-popup) with no extra keybinding
needed. When a linter supports it (`stdin = true`, see below), it also reruns live as you
type, against the buffer's current unsaved content — not just after `:w`. Either way, runs
are asynchronous and never block editing or saving.

### Built-in defaults

| Extensions | Linter | Live (as-you-type) |
|-----------|--------|---------------------|
| `.go` | `golangci-lint run --output.json.path=stdout {file}` | no — save-triggered only |
| `.js` `.jsx` `.ts` `.tsx` | `eslint --stdin --stdin-filename {file} --format json` | yes |
| `.py` | `ruff check --stdin-filename {file} --output-format json -` | yes |
| `.rs` | `cargo clippy --message-format json` (whole crate, not a single file) | no — save-triggered only |

golangci-lint and cargo clippy compile the project to produce their results, so they need a
real on-disk build and can only be run after a save. eslint and ruff can lint source handed
to them directly, so indigo pipes the live buffer content to their stdin and reruns them on
every edit (coalesced: a burst of keystrokes collapses into one rerun after the linter
finishes, not one process per keystroke).

Only linters whose command is found in PATH (or `node_modules/.bin`) are used; missing
tools are silently skipped, same as formatters.

### Custom linters

Add a `[[linter]]` block to override or extend the defaults. `format` must name one of
`internal/lint`'s registered output parsers — `golangci-lint-json`, `eslint-json`,
`ruff-json`, or `cargo-clippy-json`; a linter whose output indigo doesn't know how to
parse yet can't be added this way. Set `stdin = true` if the command reads source from
stdin (with `{file}` passed separately, purely so it can resolve config/parser by path) —
that's what enables as-you-type reruns instead of save-only ones.

```toml
[[linter]]
extensions = ["go"]
command    = "golangci-lint"
args       = ["run", "--output.json.path=stdout", "{file}"]
format     = "golangci-lint-json"
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

## Search & replace

`/` searches the current buffer, live and incremental — matches highlight and the cursor
jumps to the nearest one as you type (`n`/`N` repeat forward/backward, `Esc` cancels and
restores the cursor). Prefix the query with `\` for a Go-regexp search instead of literal
smart-case text (e.g. `\[0-9]+`). Since the leading `\` marks regex mode, patterns needing a
literal backslash escape (like `\d` for digits) must use the character-class form (`[0-9]`)
or write the double backslash (`\\d`) instead.

Typing a second, unescaped `/` — `/pattern/replacement` — turns the same search into a live
search-and-replace preview: every match is highlighted red and its computed replacement is
shown inline in green right after it (like a diff), updating as you keep typing. `Enter`
applies every previewed replacement as one undo step; `Esc` cancels with no changes. A
literal `/` inside `pattern` is written `\/` — once past the delimiter, everything else typed
is the replacement verbatim. With a regex pattern, the replacement can reference captured
groups Go-style — `$1`, `$2`, `${name}`:

```
/\(\w+)-(\w+)/$2-$1      " preview: swap-hyphenated → hyphenated-swap, Enter to apply
```

An empty replacement (`/pattern/`) previews deleting every match.

Both plain search and search-and-replace scope to the active selection instead of the whole
buffer when one exists — select a line with `x` (or extend across several) to search or
replace only within it, or make any selection first. A replace commit clears the selection
afterward, since its bounds no longer necessarily mean anything once the text has changed.

For search/replace across the whole workspace instead of one buffer, see `:grep`/`:find` and
the search & replace dialog (Command menu: `Space`, then `s`).

## Key bindings

Rebind an existing Normal- or Insert-mode action to a different key, or bind an additional
key to it, with one `[[keybind]]` block per binding:

```toml
[[keybind]]
mode   = "normal"
key    = "ctrl+p"
action = "open-file-picker"
```

`mode` is `"normal"` or `"insert"`. `key` follows the same names Bubble Tea reports
(`"ctrl+p"`, `"shift+up"`, `"alt+s"`, plain characters like `"j"`, ...). `action` must be one
of the built-in action names below. A key that's currently a multi-key prefix menu (like `g`
or `m`) can't be overridden this way — indigo logs a startup warning and leaves it alone.
An unknown `mode` or `action` is likewise reported as a warning rather than silently ignored.

### Normal-mode actions (default key)

| Action | Default key | Action | Default key |
| --- | --- | --- | --- |
| `open-file-picker` | `ctrl+p` | `search-next` | `n` |
| `search-previous` | `N` | `insert-before-cursor` | `i` |
| `quit-hint` | `ctrl+c` | `command-mode` | `:` |
| `search` | `/` | `cancel-selection` | `esc` |
| `append-after-cursor` | `a` | `append-line-end` | `A` |
| `open-line-below` | `o` | `open-line-above` | `O` |
| `save` | `ctrl+s` | `hover-docs` | `K` |
| `join-lines` | `J` | `toggle-diagnostics-popup` | `D` |
| `undo` | `u` | `redo` | `U` |
| `extend-next-word-start` | `W` | `extend-word-forward` | `E` |
| `select-line` | `x` | `extend-line-backward` | `X` |
| `select-all` | `%` | `clear-selections` | `;` |
| `flip-selection` | `alt+;` | `delete-selection` | `d` |
| `change-selection` | `c` | `yank` | `y` |
| `cursor-left` | `h`, `left` | `cursor-right` | `l`, `right` |
| `cursor-down` | `j`, `down` | `cursor-up` | `k`, `up` |
| `page-down` | `ctrl+f`, `pgdown` | `page-up` | `ctrl+b`, `pgup` |
| `go-to-last-line` | `G` | `go-to-line-start` | `0`, `home` |
| `go-to-first-non-blank` | `^` | `go-to-line-end` | `$`, `end` |
| `next-word-start` | `w` | `previous-word-start` | `b` |
| `word-end` | `e` | `paste` | `p` |
| `extend-word-backward` | `B` | `select-next-occurrence` | `ctrl+d` |
| `add-cursor-below` | `C` | `toggle-comment` | `ctrl+/`, `ctrl+_` |
| `show-plugin-bindings` | `?` | `split-selection-into-cursors` | `alt+s` |
| `set-mark` | `z` | `select-to-mark` | `Z` |
| `indent` | `>` | `unindent` | `<` |
| `move-line-up` | `shift+up` | `move-line-down` | `shift+down` |
| `jump-back` | `-` | `jump-forward` | `=`, `+` |

Multi-key sequences (the `g`, `m`, `M`, `~`, `[`, `]`, and Space menus, plus any
others added later) aren't individually overridable — only the single-key
actions listed above and in the table below.

### Insert-mode actions (default key)

| Action | Default key | Action | Default key |
| --- | --- | --- | --- |
| `trigger-completion` | `ctrl+@`, `ctrl+space` | `exit-insert-mode` | `esc`, `ctrl+c` |
| `save` | `ctrl+s` | `backspace` | `backspace` |
| `delete-forward` | `delete` | `newline` | `enter` |
| `insert-tab` | `tab` | `cursor-left` | `left` |
| `cursor-right` | `right` | `cursor-up` | `up` |
| `cursor-down` | `down` | `line-start` | `home` |
| `line-end` | `end` | | |

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
insert_cursor_bg = "#AAFFAA"  # insert-mode cursor fill color (normal mode's cursor is reverse-video, no fixed color)
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
match_pair_fg   = "#AAAAAA"   # underlines the other half of a bracket/quote pair under the cursor
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
picker_ignore_dirs = ["build", "dist"]
format_on_save = true
bracket_colors = true
indent_guides  = true
inlay_hints    = true
semantic_tokens = true
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
