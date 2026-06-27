# indigo

A terminal text editor written in Go using a client/server model via Cap'n Proto RPC.

## Architecture

indigo separates the editor into a **server** and one or more **clients** communicating over a Unix domain socket. The server owns all open buffers, runs language servers (LSP), and handles crash recovery. Each terminal window is a lightweight client that renders the UI and sends edits.

The client–server protocol is built on **[Cap'n Proto](https://capnproto.org)**, chosen specifically for its performance characteristics:

- **Zero-copy serialization** — Cap'n Proto's wire format *is* the in-memory representation. There is no parsing or marshaling step; the server can hand a response directly to the network layer without touching each byte.
- **Negligible latency** — Because there is no encode/decode overhead, a round-trip for a keystroke or an LSP hover request adds no measurable latency on top of the Unix socket itself.
- **Schema-defined interface** — The RPC interface is described in a `.capnp` schema file, giving both sides a typed, versioned contract with built-in support for backwards-compatible evolution.

The practical result is that multiple `io` windows on the same workspace share one server process with no perceptible communication cost — edits, diagnostics, and completions feel as immediate as if everything were running in a single process.

## Usage

```
io <file>
io .            # open directory (shows file picker)
io +42 file.go  # open at line 42
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
