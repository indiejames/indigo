# Language Support

## Syntax highlighting

Syntax highlighting is provided via Tree-sitter grammars and is available for the following languages:

| Extension(s) | Language |
|---|---|
| `.s` `.asm` | Assembly |
| `.sh` `.bash` | Bash |
| `.c` `.h` | C |
| `.cc` `.cpp` `.cxx` `.c++` `.hpp` | C++ |
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
| `.gd` | Godot's built-in GDScript server | requires the Godot editor to already be running with the project open — see `address` in [configuration.md](configuration.md#tcp-backed-servers-gdscript--godot); indigo cannot launch Godot itself |

## Auto-formatting

Formatters run on `:fmt` and (when enabled) automatically on save. Only tools found in PATH are used; missing tools are silently skipped.

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
