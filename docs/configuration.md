# Configuration

Config file location: `~/.config/indigo/config.toml` (created automatically on first run if absent; all settings have defaults).

## General options

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `line_numbers` | bool | `true` | Show line numbers in the gutter |
| `hide_tabs` | bool | `false` | Hide the tab bar even when multiple buffers are open |
| `fuzzy_search` | bool | `true` | Use fuzzy matching in the file picker |
| `format_on_save` | bool | `false` | Run the file's formatter automatically on `:w` |
| `recovery_interval_secs` | int | `5` | How often (in seconds) unsaved content is written to the recovery directory |
| `recovery_max_bytes` | int | `104857600` | Maximum file size (bytes) eligible for crash recovery (default 100 MB) |

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

## Full example

```toml
line_numbers   = true
hide_tabs      = false
fuzzy_search   = true
format_on_save = true

[[language_server]]
extensions = ["ml", "mli"]
command    = "ocamllsp"

[[formatter]]
extensions = ["js", "ts", "jsx", "tsx"]
command    = "biome"
args       = ["format", "--stdin-file-path", "{file}"]
```
