# Language Support

Four separate mechanisms decide what indigo can do with a given file, and they are keyed
independently — a language can have highlighting but no language server, a formatter but no
linter, and so on:

| | Comes from | Selected by |
|---|---|---|
| [Syntax highlighting](#syntax-highlighting) | A Tree-sitter grammar compiled into the binary | File extension (or filename) |
| [LSP](#lsp-support) | A language server found on `PATH` | File extension |
| [Formatting](#auto-formatting) | An external tool on `PATH` or in `node_modules/.bin`, falling back to the language server | File extension |
| [Linting](#linting) | An external tool on `PATH` or in `node_modules/.bin` | File extension |

Everything on this page is a *default*. Any of it can be overridden, and new entries added,
in `config.toml` — see [Configuration](configuration.md).

## Syntax highlighting

Syntax highlighting is provided via Tree-sitter grammars and is available for the following languages:

| Extension(s) | Language |
|---|---|
| `.s` `.asm` | Assembly |
| `.sh` `.bash` | Bash |
| `.c` `.h` | C |
| `.cc` `.cpp` `.cxx` `.c++` `.hh` `.hpp` `.hxx` | C++ |
| `.cs` | C# |
| `.clj` `.cljs` `.cljc` `.edn` | Clojure |
| `.css` | CSS |
| `.cue` | CUE |
| `.dart` | Dart |
| `Dockerfile` | Dockerfile |
| `.ex` `.exs` | Elixir |
| `.elm` | Elm |
| `.erl` `.hrl` | Erlang |
| `.fish` | Fish |
| `.gd` | GDScript |
| `COMMIT_EDITMSG` `MERGE_MSG` `SQUASH_MSG` `TAG_EDITMSG` `MERGE_HEAD` | Git commit message |
| `.gleam` | Gleam |
| `.go` | Go |
| `.graphql` `.gql` | GraphQL |
| `.groovy` `.gvy` `.gy` `.gsh` | Groovy |
| `.hs` `.lhs` | Haskell |
| `.html` `.htm` | HTML |
| `.java` | Java |
| `.js` `.mjs` `.cjs` | JavaScript |
| `.json` | JSON |
| `.jl` | Julia |
| `.kt` `.kts` | Kotlin |
| `.lua` | Lua |
| `Makefile` `.mk` `.mak` `.make` | Make |
| `.md` `.markdown` | Markdown |
| `.ml` `.mli` | OCaml |
| `.nim` | Nim |
| `.nix` | Nix |
| `.php` | PHP |
| `.proto` | Protobuf |
| `.py` | Python |
| `.r` | R |
| `.rb` | Ruby |
| `.rs` | Rust |
| `.scala` `.sc` | Scala |
| `.sql` | SQL |
| `.svelte` | Svelte |
| `.swift` | Swift |
| `.tf` `.hcl` | HCL / Terraform |
| `.toml` | TOML |
| `.ts` | TypeScript |
| `.tsx` | TSX |
| `.yaml` `.yml` | YAML |
| `.zig` | Zig |

Filename matches (`Dockerfile`, `Makefile`, the git commit-message files) are
case-insensitive and take precedence over the extension.

A file whose extension resolves to nothing falls back to two more tries before plain text:
a `#!` shebang line is sniffed for a known interpreter, and `:set ft=<lang>` overrides both
(see [File type aliases](configuration.md#file-type-aliases) to make an override permanent).

**Grammars are compiled in, not loaded at runtime.** Which ones a binary has is fixed by
its build tags: `make build` / `make install` include all of them, `make build-minimal`
includes none, and `make build-custom LANGS="lang_go lang_rust"` includes exactly what you
name. If a file you expect to be highlighted isn't, check which build you're running before
anything else.

## LSP support

Language servers are started automatically when you open a file with a matching extension, provided the server binary is in your PATH. See [Configuration](configuration.md) for how to override or add servers.

| Extensions | Default server | Install |
|-----------|---------------|---------|
| `.go` | `gopls` | `go install golang.org/x/tools/gopls@latest` |
| `.rs` | `rust-analyzer` | via `rustup component add rust-analyzer` |
| `.ts` `.tsx` `.js` `.jsx` | `typescript-language-server` | `npm install -g typescript-language-server typescript` |
| `.py` | `pylsp` | `pip install python-lsp-server` |
| `.c` `.cpp` `.h` `.hpp` | `clangd` | via your system package manager |
| `.lua` | `lua-language-server` | [github.com/LuaLS/lua-language-server](https://github.com/LuaLS/lua-language-server) |
| `.rb` | `solargraph` | `gem install solargraph` |
| `.java` | `jdtls` | [github.com/eclipse-jdtls/eclipse.jdt.ls](https://github.com/eclipse-jdtls/eclipse.jdt.ls) |
| `.zig` | `zls` | [github.com/zigtools/zls](https://github.com/zigtools/zls) |
| `.gd` | Godot's built-in GDScript server | not a built-in default — requires an explicit `[[language_server]]` block with `address` set (indigo has no address to guess) and the Godot editor already running with the project open before indigo attempts to connect; see [configuration.md](configuration.md#tcp-backed-servers-gdscript--godot). Indigo cannot launch Godot itself |

## Auto-formatting

Formatters run on `:fmt`, and on save when `format_on_save = true`. Only tools found on
`PATH` — or in a `node_modules/.bin` reachable from the file, resolved per file so a
monorepo's non-hoisted install is still found — are used; missing tools are silently
skipped.

| Extensions | Default formatter | Install |
|-----------|-----------------|---------|
| `.go` | `gofmt` | included with Go |
| `.rs` | `rustfmt` | `rustup component add rustfmt` |
| `.py` | `black` | `pip install black` |
| `.js` `.jsx` `.ts` `.tsx` `.css` `.html` `.json` `.yaml` `.yml` `.md` | `prettier` | `npm install -g prettier` |
| `.c` `.cpp` `.h` `.hpp` | `clang-format` | via your system package manager |
| `.sh` `.bash` | `shfmt` | `go install mvdan.cc/sh/v3/cmd/shfmt@latest` |
| `.lua` | `stylua` | [github.com/JohnnyMorganz/StyLua](https://github.com/JohnnyMorganz/StyLua) |
| `.zig` | `zig fmt` | included with Zig |
| `.nix` | `nixpkgs-fmt` | `nix-env -i nixpkgs-fmt` |
| `.toml` | `taplo` | [taplo.tamasfe.dev](https://taplo.tamasfe.dev) |
| `.swift` | `swiftformat` | `brew install swiftformat` |
| `.rb` | `rubocop` | `gem install rubocop` |
| `.java` | `google-java-format` | [github.com/google/google-java-format](https://github.com/google/google-java-format) |

A formatter is looked up in this order: a `[[formatter]]` block you configured, then the
table above (found on `PATH` or in the project's `node_modules/.bin`), then the file's own
language server's formatting request. External tools are preferred over LSP formatting
because they honour project-local config files — `.prettierrc`, `rustfmt.toml`,
`.clang-format` — that a language server may not read. If none of the three produces a
formatter, `:fmt` reports that there is nothing to run.

See [Formatters](configuration.md#formatters) to add your own, and
[Language-server formatting](configuration.md#language-server-formatting-the-fallback) to
tune the fallback.

## Linting

Linters run on save, asynchronously, and their findings are merged with the file's LSP
diagnostics — same gutter markers, same `D` popup, same status-bar counts. As with
formatters, only tools found on `PATH` or in `node_modules/.bin` are used, and a missing
tool is silently skipped.

| Extensions | Default linter | Runs | Install |
|-----------|---------------|------|---------|
| `.go` | `golangci-lint` | on save | [golangci-lint.run](https://golangci-lint.run) |
| `.js` `.jsx` `.ts` `.tsx` | `eslint` | live, as you type | `npm install -D eslint` |
| `.py` | `ruff` | live, as you type | `pip install ruff` |
| `.rs` | `cargo clippy` | on save | `rustup component add clippy` |

The "runs" column is not a preference — it follows from how the tool works. A linter that
accepts source on stdin (`eslint`, `ruff`) can lint the buffer you're typing in, so it runs
on every edit. A compile-based linter (`golangci-lint`, `cargo clippy`) needs a real build
from a real file on disk, so it can only run after a save. `cargo clippy` in particular
lints the whole crate containing the saved file, not that file alone.

All four also define a whole-project invocation used by `:diagnostics` — see
[Workspace diagnostic scan](configuration.md#workspace-diagnostic-scan).

Unlike formatting, there is no language-server fallback here: when nothing matches, LSP
diagnostics are simply all you get. See [Linters](configuration.md#linters) to add your own.
