# Plugin Architecture

## Goals

- Plugins can do anything a VS Code extension can do, within the limits of a TUI application
- A misbehaving or slow plugin must never freeze or slow down the editor
- Plugin authors write Go; Cap'n Proto details are hidden behind an SDK
- Plugins are distributable as pre-compiled binaries (no Go toolchain required to install)

## Process model

Each plugin runs as a **separate OS process**, started by the indigo server when the editor opens a workspace. This is the same lesson VS Code learned from Atom: in-process extensions can bring the editor to a crawl; out-of-process extensions are isolated by the OS.

```
┌──────────────────────────────────────────────┐
│                indigo server                 │
│                                              │
│  ┌─────────────┐    ┌────────────────────┐  │
│  │   plugin    │    │   editor service   │  │
│  │   manager   │    │  (buffers, LSP,    │  │
│  └──────┬──────┘    │   formatting, …)   │  │
│         │ Cap'n     └────────────────────┘  │
│         │ Proto                              │
└─────────┼────────────────────────────────────┘
          │  Unix domain sockets (one per plugin)
    ┌─────┴──────────────────────────┐
    │                                │
┌───▼──────────────┐  ┌─────────────▼────────────┐
│  plugin process  │  │    plugin process         │
│  (jumpy)         │  │    (git-blame)            │
└──────────────────┘  └──────────────────────────┘
```

One process per plugin. If a plugin crashes or hangs, only that plugin is affected.

## Communication: Cap'n Proto over Unix sockets

Communication uses **Cap'n Proto RPC** over a per-plugin Unix domain socket. This provides:
- Zero-copy serialization (no JSON parsing overhead)
- Typed, schema-defined interfaces
- Native two-directional RPC (both sides can call into the other)
- Promise pipelining (async by default at the protocol level)

Plugin authors never touch Cap'n Proto directly. The **indigo plugin SDK** (a separate Go module: `github.com/indiejames/indigo-plugin-sdk`) wraps everything behind a clean Go interface.

### Why not stdio / JSON?

Stdio with JSON (the LSP model) works well for request/response tools. Plugin communication is more complex: the server needs to call into plugins (dispatch events) AND plugins need to call back into the server (apply edits, register commands). Cap'n Proto's two-sided interface model handles this natively; a JSON/stdio protocol would require multiplexing both directions manually.

## The capability pattern (not a fat interface, not a loose event bus)

Rather than defining a large interface that every plugin must implement, or an untyped event bus, indigo uses **Cap'n Proto's object-capability model**.

During initialization, a plugin receives an `EditorApi` capability and uses it to **register specific handler objects** for only the events it cares about. The server holds references to those handler objects and calls into them when relevant events occur.

```
Plugin.initialize(api: EditorApi):
    api.registerKeyBinding("ctrl+j", myKeyHandler)
    api.registerBufferChangeHandler(myBufferHandler)
    api.registerCommand("blame", myBlameHandler)
    # Only these handlers will ever be called
```

This means:
- The `Plugin` interface has **one method** (`initialize`) — no fat interface
- Adding new event types to `EditorApi` is backward-compatible — existing plugins ignore new registration methods they don't call
- Each handler is a small, focused object with one `handle` method
- The server only routes events to plugins that registered for them

## Keeping the editor snappy

The render loop and keypress path **never block** on plugin I/O. Snappiness is enforced at the architecture level, not by convention:

| Event type | Delivery | Timeout |
|---|---|---|
| Buffer opened / closed | Fire and forget | none |
| Buffer changed | Fire and forget | none |
| Buffer saved | Fire and forget | none |
| Cursor moved | Fire and forget (throttled) | none |
| Gutter decorations | Async, cached last result | none |
| Overlay / virtual text | Async, cached last result | none |
| Status bar items | Async, cached last result | none |
| **Key binding handler** | Await response | 30 ms |
| **Insert-mode hook** (e.g. bracket close) | Await response | 30 ms |
| **Command handler** (`:blame`) | Await response | 5 s |

For the two interactive cases (key bindings and insert hooks), the server dispatches to the plugin in a goroutine and awaits the response with a deadline. If the deadline expires, the keypress falls through as if no plugin handled it. A plugin that consistently times out gets its key binding de-registered until it recovers.

Decoration updates (gutter annotations, overlays, status bar items) are delivered asynchronously. The editor renders its last cached decoration state every frame; stale decorations are preferable to a frozen editor.

Overlay decorations are rendered in a single pass through each visible line's runes — labels are injected directly at their target column during the normal character-rendering loop, with no ANSI post-processing. Rendering cost is O(visible_lines × line_width) regardless of how many overlay decorations the plugin returns, so a plugin returning 500 overlays costs no more than one returning 5.

## What plugins can do

### UI contributions

| Surface | Description |
|---|---|
| Gutter decorations | Per-line icons or text (git blame, error counts) |
| Inline overlays | Virtual text overlaid on buffer content (jumpy labels, type hints) |
| Status bar items | Text segments in the mode line |
| Popup / hover augmentation | Extra content added to the K hover popup |

### Editor interactions

| API | Description |
|---|---|
| Apply text edits | Insert, delete, or replace ranges in a buffer |
| Move cursor | Jump to a position or selection |
| Open file | Open a file in a new buffer |
| Show message | Write to the status bar transiently |
| Register key binding | Own a key sequence in normal or insert mode |
| Register command | Add a `:commandname` callable from command mode |
| Register insert hook | Intercept a specific character in insert mode |

### Workspace access

| API | Description |
|---|---|
| Read buffer content | Get full text or a line range |
| Buffer events | Subscribe to open / change / save / close |
| Run external process | Spawn a shell command and receive stdout (for git, etc.) |
| Read files | Read arbitrary workspace files |

## SDK design

Plugin authors implement a single interface and call `sdk.Run()`:

```go
package main

import sdk "github.com/indiejames/indigo-plugin-sdk"

type JumpyPlugin struct{ labels map[string]sdk.Pos }

func (p *JumpyPlugin) Initialize(api sdk.EditorAPI) sdk.PluginInfo {
    api.RegisterKeyBinding("ctrl+j", p.onTrigger)
    return sdk.PluginInfo{Name: "jumpy", Version: "1.0.0"}
}

func main() { sdk.Run(&JumpyPlugin{}) }
```

The SDK handles:
- Connecting to the server socket (path passed as a CLI argument by the plugin manager)
- Cap'n Proto serialization / deserialization
- Goroutine management for concurrent handler calls
- Graceful shutdown

## Cap'n Proto schema (outline)

```capnp
interface Plugin {
    initialize @0 (api: EditorApi) -> (info: PluginInfo);
}

interface EditorApi {
    # Registration (called during initialize)
    registerKeyBinding    @0 (trigger: Text, handler: KeyHandler) -> ();
    registerInsertHook    @1 (char: Text,    handler: KeyHandler) -> ();
    registerCommand       @2 (name: Text,    handler: CommandHandler) -> ();
    registerBufferHandler @3 (handler: BufferEventHandler) -> ();
    registerDecorations   @4 (provider: DecorationProvider) -> ();

    # Editor effects (callable any time)
    applyEdit    @5 (bufId: UInt32, edits: List(TextEdit)) -> ();
    moveCursor   @6 (bufId: UInt32, pos: Position) -> ();
    openFile     @7 (path: Text, line: UInt32) -> ();
    showMessage  @8 (text: Text) -> ();
    runProcess   @9 (cmd: Text, args: List(Text)) -> (stdout: Text, stderr: Text, exitCode: Int32);

    # Document model queries
    readBuffer      @10 (bufId: UInt32) -> (content: Text);
    readLines       @11 (bufId: UInt32, startLine: UInt32, endLine: UInt32) -> (lines: List(Text));
    readRange       @12 (bufId: UInt32, from: Position, to: Position) -> (text: Text);
    wordAt          @13 (bufId: UInt32, pos: Position) -> (start: Position, end: Position, found: Bool);
    bufferInfo      @14 (bufId: UInt32) -> (path: Text, languageId: Text, lineCount: UInt32, isDirty: Bool);
    visibleRange    @15 (clientId: UInt64) -> (startLine: UInt32, endLine: UInt32);
}

# Handler interfaces — implemented by the plugin, called by the server
interface KeyHandler {
    handle @0 (key: Text, ctx: KeyContext) -> (response: KeyResponse);
}
interface CommandHandler {
    handle @0 (args: List(Text), ctx: CommandContext) -> ();
}
interface BufferEventHandler {
    onOpen   @0 (event: BufferEvent) -> ();
    onChange @1 (event: BufferEvent) -> ();
    onSave   @2 (event: BufferEvent) -> ();
    onClose  @3 (event: BufferEvent) -> ();
}
interface DecorationProvider {
    getDecorations @0 (bufId: UInt32, visibleRange: Range) -> (decorations: List(Decoration));
}
```

## Distribution

Plugins are standalone executables installed in `~/.config/indigo/plugins/`.

Each plugin directory contains:
```
~/.config/indigo/plugins/
  jumpy/
    plugin.toml       # name, version, description, binary paths
    jumpy-darwin-arm64
    jumpy-darwin-amd64
    jumpy-linux-amd64
    jumpy-linux-arm64
```

`plugin.toml`:
```toml
name    = "jumpy"
version = "1.0.0"
description = "Jump to any visible word with two keystrokes"

[binaries]
"darwin/arm64" = "jumpy-darwin-arm64"
"darwin/amd64" = "jumpy-darwin-amd64"
"linux/amd64"  = "jumpy-linux-amd64"
"linux/arm64"  = "jumpy-linux-arm64"
```

The plugin manager selects the correct binary for the current `GOOS/GOARCH` at startup.

A future `io plugin install <source>` command would automate downloading and unpacking plugin releases.

## Plugin lifecycle

1. Server starts; reads `~/.config/indigo/plugins/`
2. For each valid plugin, spawns the binary: `./jumpy-darwin-arm64 --socket /tmp/indigo-<workspace>/plugin-jumpy.sock`
3. Plugin connects to the socket and calls `Plugin.initialize(api)`
4. Plugin registers handlers; server stores capability references
5. Server routes events to relevant plugins throughout the session
6. On server shutdown (all clients disconnected), sends shutdown signal; plugins have 2s to exit cleanly before `SIGKILL`

## Document model

Plugins access buffer content through the Cap'n Proto document query API — they never touch the rope or gap buffer directly. The server-side `Buffer` type exposes the following operations to the plugin layer:

| Query | Description |
| ----- | ----------- |
| `readBuffer(bufId)` | Full text of the buffer |
| `readLines(bufId, start, end)` | Text of a line range (efficient for large files) |
| `readRange(bufId, from, to)` | Text of an arbitrary (line, col) → (line, col) range |
| `wordAt(bufId, pos)` | Start and end position of the word at a cursor position |
| `bufferInfo(bufId)` | Path, language ID, line count, dirty flag |
| `visibleRange(clientId)` | First and last visible line in a connected client's viewport |

`wordAt` and `readRange` require two small additions to `internal/document/buffer.go` (`TextRange` and `WordAt` methods). The internal machinery (`logicalSlice`, `logicalOffset`) is already present; these are thin wrappers over it.

## Client/server boundary

**All plugins are server-side.** There is one plugin system, one SDK, one socket per plugin.

The apparent problem with this — that jumpy-style plugins need to know what's *visible* on screen, which is client state — is solved by **viewport synchronization**: the client sends `{topLine, height, width}` to the server whenever scroll position or window dimensions change. The server stores this per connected client. Plugins then call `visibleRange(clientId)` to find out which lines are on screen.

The data flow for an overlay plugin like jumpy:

```text
1. ctrl+j keypress → server → jumpy plugin
2. Plugin calls visibleRange() → gets lines 40–80
3. Plugin calls readLines(bufId, 40, 80) → gets text
4. Plugin computes label map, returns overlay decorations + enters capture mode
5. Server caches overlay, pushes decoration update to client
6. Client renders label overlays: each label is injected inline during the rune-rendering pass at its target column — no separate overlay compositing step
7. User types "ab" → server routes to jumpy (in capture mode)
8. Plugin returns moveCursor{line: 52, col: 7} + clears overlay
```

All steps cross Unix domain sockets, adding microseconds of latency — imperceptible to users.

Keeping all plugins server-side avoids the complexity of two plugin systems and two SDKs, and eliminates the coordination problem for plugins that need both UI (overlays) and buffer access (edits) — which is most non-trivial plugins.

## Open questions

- **Plugin sandboxing**: plugins have full OS access. This is consistent with VS Code's current model. Sandboxing (e.g. via WASM) is a possible future direction but is not in scope for the initial implementation.
- **Event bus vs capability pattern**: the design above uses the capability pattern. An alternative is a pub/sub event bus where plugins subscribe to named event topics and the server fans out. This is simpler to implement but loses the type safety and backpressure properties of the capability approach. Decision pending.
