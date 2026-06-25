# Indigo

A terminal text editor written in Go using a client/server model via Cap'n Proto RPC.

## Usage

```
io <file>
```

## Syntax Highlighting

| Extension(s)                      | Language        |
|-----------------------------------|-----------------|
| `.s` `.asm`                       | Assembly        |
| `.sh` `.bash`                     | Bash            |
| `.c` `.h`                         | C               |
| `.clj` `.cljs` `.cljc` `.edn`     | Clojure         |
| `.css`                            | CSS             |
| `.cue`                            | CUE             |
| `.cc` `.cpp` `.cxx` `.c++` `.hpp` | C++             |
| `.cs`                             | C#              |
| `.dart`                           | Dart            |
| `Dockerfile`                      | Dockerfile      |
| `.ex` `.exs`                      | Elixir          |
| `.elm`                            | Elm             |
| `.erl` `.hrl`                     | Erlang          |
| `.gd`                             | GDScript        |
| `.gleam`                          | Gleam           |
| `.go`                             | Go              |
| `.graphql` `.gql`                 | GraphQL         |
| `.groovy` `.gvy` `.gy` `.gsh`     | Groovy          |
| `.hs` `.lhs`                      | Haskell         |
| `.html` `.htm`                    | HTML            |
| `.java`                           | Java            |
| `.js` `.mjs` `.cjs`               | JavaScript      |
| `.json`                           | JSON            |
| `.jl`                             | Julia           |
| `.kt` `.kts`                      | Kotlin          |
| `.lua`                            | Lua             |
| `.md` `.markdown`                 | Markdown        |
| `.ml` `.mli`                      | OCaml           |
| `.nim`                            | Nim             |
| `.nix`                            | Nix             |
| `.php`                            | PHP             |
| `.proto`                          | Protobuf        |
| `.py`                             | Python          |
| `.r`                              | R               |
| `.rb`                             | Ruby            |
| `.rs`                             | Rust            |
| `.scala` `.sc`                    | Scala           |
| `.sql`                            | SQL             |
| `.svelte`                         | Svelte          |
| `.swift`                          | Swift           |
| `.tf` `.hcl`                      | HCL / Terraform |
| `.toml`                           | TOML            |
| `.ts`                             | TypeScript      |
| `.tsx`                            | TSX             |
| `.yaml` `.yml`                    | YAML            |
| `.zig`                            | Zig             |

## Configuration

Config file: `~/.config/indigo/config.toml`

```toml
line_numbers = true  # default: true
```
