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
│  ┌─────────────┐    ┌────────────────────┐   │
│  │   plugin    │    │   editor service   │   │
│  │   manager   │    │  (buffers, LSP,    │   │
│  └──────┬──────┘    │   formatting, …)   │   │
│         │ Cap'n     └────────────────────┘   │
│         │ Proto                              │
└─────────┼────────────────────────────────────┘
          │  Unix domain sockets (one per plugin)
    ┌─────┴──────────────────────────┐
    │                                │
┌───▼──────────────┐  ┌─────────────▼────────────┐
│  plugin process  │  │    plugin process        │
│  (jumpy)         │  │    (git)                 │
└──────────────────┘  └──────────────────────────┘
```

One process per plugin. If a plugin crashes or hangs, only that plugin is affected.

## Communication: Cap'n Proto over Unix sockets

Communication uses **Cap'n Proto RPC** over a per-plugin Unix domain socket. This provides:
- Zero-copy serialization (no JSON parsing overhead)
- Typed, schema-defined interfaces
- Native two-directional RPC (both sides can call into the other)
- Promise pipelining (async by default at the protocol level)

Plugin authors never touch Cap'n Proto directly. The **indigo plugin SDK** (package `github.com/indiejames/indigo/sdk`, in this repo) wraps everything behind a clean Go interface.

### Why not stdio / JSON?

Stdio with JSON (the LSP model) works well for request/response tools. Plugin communication is more complex: the server needs to call into plugins (dispatch events) AND plugins need to call back into the server (apply edits, register commands). Cap'n Proto's two-sided interface model handles this natively; a JSON/stdio protocol would require multiplexing both directions manually.

## The capability pattern (not a fat interface, not a loose event bus)

Rather than defining a large interface that every plugin must implement, or an untyped event bus, indigo uses **Cap'n Proto's object-capability model**.

During initialization, a plugin receives an `EditorApi` capability and uses it to register specific handler objects for only the events it cares about. The server holds references to those handler objects and calls into them when relevant events occur.

```
Plugin.initialize(api: EditorApi):
    api.registerKeyBinding("ctrl+j", myKeyHandler)
    api.registerBufferHandler(myBufferHandler)
    api.registerCommand("blame", myBlameHandler)
    api.registerMenuAction("myplugin.blame", myBlameMenuHandler)
    # Only these handlers will ever be called
```

This means:
- The `Plugin` interface has **one method** (`initialize`) — no fat interface
- Adding new event types to `EditorApi` is backward-compatible — existing plugins ignore new registration methods they don't call
- Each handler is a small, focused object with one `handle` method
- The server only routes events to plugins that registered for them

## Keeping the editor snappy

The render loop and keypress path **never block** on plugin I/O. Snappiness is enforced at the architecture level, not by convention:

| Event type                                | Delivery                    | Timeout |
|-------------------------------------------|-----------------------------|---------|
| Buffer opened / closed                    | Fire and forget             | none    |
| Buffer changed                            | Fire and forget             | none    |
| Buffer saved                              | Fire and forget             | none    |
| Cursor moved                              | Fire and forget (throttled) | none    |
| Gutter decorations                        | Async, cached last result   | none    |
| Overlay / virtual text                    | Async, cached last result   | none    |
| Status bar items                          | Async, cached last result   | none    |
| Diagnostics (`PublishDiagnostics`)        | Push, cached until republished or the buffer closes | none |
| Workspace diagnostics (`PublishWorkspaceDiagnostics`) | Push, cached until republished; run from an `OnWorkspaceScan` handler | 2 min per plugin per scan |
| **Key binding handler**                   | Await response              | 300 ms  |
| **Insert-mode hook** (e.g. bracket close) | Fire-and-forget notification after char is inserted; response applied if it arrives | 300 ms |
| **Menu action handler** (Command menu)    | Await response              | 300 ms  |
| **Action provider** (Shift+F actions)     | Await response              | 500 ms  |
| **Fix provider** (fixable decorations)    | Await response              | 500 ms  |
| **Command handler** (`:name`)             | Registered, not yet dispatched — see note | — |

For the interactive cases (key bindings and menu actions), the server dispatches to the plugin in a goroutine and awaits the response with a deadline. If the deadline expires, the keypress falls through as if no plugin handled it.

**Insert-mode hooks are different on purpose.** Every keystroke goes through `insertSelfInsert`, so gating the character's own insertion on a plugin round trip would risk stalling — or on a timeout, silently dropping — ordinary typing. Instead the client inserts the typed char locally and immediately, exactly as if no hook were registered, and *also* fires the registered `OnInsert` handler (if any) as a background request with the same 300ms deadline as other handlers. If a response comes back in time, it's applied the same way a key binding's response is (decoration refresh, optional cursor move, optional capture mode) — it just never blocks or overrides the insert itself. Use `OnInsert` for per-char triggers (e.g. re-checking something after `.` or `(` is typed); use `OnChange` (fires on every edit, no char filtering) if you need to react to *all* typing.

> **Note:** `registerCommand`/`OnCommand` handlers are stored server-side but nothing currently calls them — the client's `:name` command line does not yet route to plugin-registered commands. This is a known gap, not a design choice; treat `OnCommand` as reserved for now.

Decoration updates (gutter annotations, overlays, status bar items) are polled by the client (every 3rd tick of its 120ms timer, ~360ms average) and rendered from that cache every frame — stale decorations are preferable to a frozen editor. A plugin that needs a decoration change to show up sooner than the next poll — e.g. after finishing async work like an LLM completion — can call the SDK's `RefreshDecorations(bufID)`, which pushes an immediate refetch to any client currently viewing that buffer instead of waiting on the poll cadence.

Diagnostics work differently from decorations even though both are "async, cached": a decoration provider is *pulled* by the server on every client poll (bounded by a 200ms per-plugin timeout — see `GetDecorations` in `internal/plugin/manager.go`), so a slow computation just returns stale data next tick. Diagnostics have no pull counterpart at all — `PublishDiagnostics(bufID, version, diags)` is a one-way push the plugin calls whenever it finishes computing (e.g. after a debounced spell-check pass), and the server caches whatever was last published per plugin per buffer until either the plugin republishes or the buffer closes. This avoids forcing a possibly-slow computation (spell-checking a large file, say) into a tight poll-cycle timeout the way a pull model would. `version` must be the buffer's version — from `BufferInfo` — that the plugin actually computed diagnostics against; the server silently discards a publish whose version doesn't match the buffer's *current* version rather than let a plugin overwrite live diagnostics with results computed against content that's since changed. Published diagnostics are merged into the exact same list LSP/lint diagnostics populate — the status bar's error/warning/info counts, the diagnostics popup (Shift+E), gutter markers — with `Source` set to the publishing plugin's name; the client doesn't need to know or care where a given diagnostic came from. See `plugins/indigo-spell` for a working example (spelling issues, previously underline-decoration-only, are now also published as info-severity diagnostics).

**Workspace diagnostics extend the same push model to files nobody has open.** `PublishDiagnostics` only ever covers an open buffer — it needs a `bufId` and a `version` to check staleness against, neither of which exists for a file with no live `document.Buffer`. `PublishWorkspaceDiagnostics(path, diags)` is the path-keyed sibling: no version check (there is nothing to compare against), and one call is the authoritative diagnostic list for `(this plugin, path)` — a later call replaces the previous one, and an empty list clears it. Nothing calls this on its own; a plugin populates it from a handler registered via `OnWorkspaceScan(func())`, which the editor invokes fire-and-forget (a generous 2-minute per-plugin timeout, since walking a whole project can take real wall-clock time) at server startup and again on an explicit rescan (the diagnostic browser's `r` key) — the same two trigger points `lint.Manager.ScanWorkspace` already uses for the equivalent whole-project lint invocation. Results merge into `GetWorkspaceDiagnostics`/`GetWorkspaceDiagnosticsSummary` for any path not currently open in a buffer; an open buffer's live `PublishDiagnostics` diagnostics always supersede a stale workspace-scan entry for the same path rather than being merged alongside it. See `plugins/indigo-spell`'s `runWorkspaceScan` for a working example — it walks the project tree (skipping `.git`, `node_modules`, and similar non-source directories) reusing the exact same comment-scoping/identifier-splitting logic its per-buffer check already used, so a file shows the same spelling diagnostics whether or not anyone has it open.

Overlay decorations are rendered in a single pass through each visible line's runes — labels are injected directly at their target column during the normal character-rendering loop, with no ANSI post-processing. Rendering cost is O(visible_lines × line_width) regardless of how many overlay decorations the plugin returns, so a plugin returning 500 overlays costs no more than one returning 5.

`getCompletions` runs synchronously on every completion-popup request (the editor's own LSP-driven completions go through the identical `Complete` RPC, so a plugin's items are merged into that same list, each tagged with the plugin's name via the item's `source` field), under a bounded per-provider timeout (500ms) — a provider backed by something slow (a registry lookup, a network call) should cache results and refresh them in the background, but block *briefly* for an in-flight refresh rather than always returning empty on a cold cache: the `npm-versions` example plugin (`plugins/npm-versions`) does this — a cache miss kicks off a background fetch to `registry.npmjs.org` and waits up to ~400ms for it before falling back to "nothing yet", so the common case (a fast registry response) already has real versions to show on the very first request, not just once a second keystroke lands after the fetch quietly finished. `resolveCompletion` is a different, narrower deferral: it only ever runs once, for the single item the user has already chosen to accept, to fill in something too expensive to compute for every candidate up front (e.g. full documentation text) — it round-trips the item's opaque `data` token so the plugin can identify which candidate is being resolved, and the editor routes a `resolveCompletion` request to the language server or the right plugin based on the item's `source` field. It doesn't help with producing the candidate list itself, which is what `getCompletions` alone is responsible for.

## What plugins can do

### UI contributions

| Surface                    | Description                                                        |
|----------------------------|--------------------------------------------------------------------|
| Gutter decorations         | Per-line icons or text (git blame, error counts)                   |
| Inline overlays            | Virtual text overlaid on buffer content (jumpy labels, type hints) |
| Status bar items           | Text segments in the mode line                                     |
| Popup / hover augmentation | Extra content added to the K hover popup                           |
| Command-menu item          | An entry (or submenu) under the space Command menu                 |
| Action popup (Shift+F)     | Context-sensitive actions at the cursor position                   |
| Modal list popup           | A scrollable list the user picks from (`ShowPopup`)                |
| Input prompt               | A single-line text-input dialog (`ShowInputPrompt`)                |

### Editor interactions

| API                  | Description                                     |
|----------------------|-------------------------------------------------|
| Apply text edits     | Insert, delete, or replace ranges in a buffer   |
| Move cursor          | Jump to a position or selection                 |
| Open file            | Open a file in a new buffer                     |
| Show message         | Write to the status bar transiently             |
| Register key binding | Own a key sequence in normal mode               |
| Register command     | Add a `:commandname` callable from command mode |
| Register insert hook | Receive asynchronous notification when a specific character is typed in insert mode |
| Register menu action | Contribute an item to the space Command menu (invoked by selection, never bound to a physical key) |
| Register action provider | Contribute context-sensitive actions to the Shift+F popup |
| Register completion provider | Contribute candidates to the completion popup, merged with the buffer's language-server completions |
| Publish diagnostics  | Report issues (bufID, version, diagnostics) merged into the editor's LSP/lint diagnostics — status bar counts, diagnostics popup, gutter markers |
| Publish workspace diagnostics | Report issues for a path with no open buffer, from an `OnWorkspaceScan` handler — merged the same way, for files nobody has open |

### Workspace access

| API                  | Description                                              |
|----------------------|----------------------------------------------------------|
| Read buffer content  | Get full text or a line range                            |
| Buffer events        | Subscribe to open / change / save / close                |
| Run external process | Spawn a shell command and receive stdout (for git, etc.) |
| Read files           | Read arbitrary workspace files                           |

## SDK design

Plugin authors implement a single interface and call `sdk.Run()`:

```go
package main

import "github.com/indiejames/indigo/sdk"

type JumpyPlugin struct{}

func (p *JumpyPlugin) Init(api *sdk.Api) sdk.Info {
    api.OnMenuAction("jumpy.start", p.onTrigger) //nolint:errcheck
    return sdk.Info{Name: "jumpy", Version: "1.0.0"}
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
    registerKeyBinding     @0  (trigger: Text, handler: KeyHandler) -> ();
    registerInsertHook     @1  (char: Text,    handler: KeyHandler) -> ();
    registerCommand        @2  (name: Text,    handler: CommandHandler) -> ();
    registerBufferHandler  @3  (handler: BufferEventHandler) -> ();
    registerDecorations    @4  (provider: DecorationProvider) -> ();
    registerActionProvider @16 (provider: ActionProvider) -> ();
    registerCompletionProvider @22 (provider: CompletionProvider) -> ();
    registerMenuAction     @20 (id: Text, handler: KeyHandler) -> ();
    registerEditHandler    @18 (handler: EditEventHandler) -> ();

    # Plugin-driven UI (callable any time)
    showPopup       @17 (title: Text, items: List(PopupItem), handler: PopupHandler) -> ();
    showInputPrompt @19 (title: Text, placeholder: Text, handler: InputPromptHandler) -> ();

    # Editor effects (callable any time)
    applyEdit    @5 (bufId: UInt32, edits: List(TextEdit)) -> ();
    moveCursor   @6 (bufId: UInt32, pos: Position) -> ();
    openFile     @7 (path: Text, line: UInt32) -> ();
    showMessage  @8 (clientId: UInt64, text: Text) -> (); # clientId 0 broadcasts
    runProcess   @9 (cmd: Text, args: List(Text)) -> (stdout: Text, stderr: Text, exitCode: Int32);

    # Document model queries
    readBuffer      @10 (bufId: UInt32) -> (content: Text);
    readLines       @11 (bufId: UInt32, startLine: UInt32, endLine: UInt32) -> (lines: List(Text));
    readRange       @12 (bufId: UInt32, from: Position, to: Position) -> (text: Text);
    wordAt          @13 (bufId: UInt32, pos: Position) -> (start: Position, end: Position, found: Bool);
    bufferInfo      @14 (bufId: UInt32) -> (path: Text, languageId: Text, lineCount: UInt32, isDirty: Bool, version: UInt64);
    visibleRange    @15 (clientId: UInt64) -> (startLine: UInt32, endLine: UInt32);

    # Push an immediate decoration refetch to clients viewing bufId
    refreshDecorations @21 (bufId: UInt32) -> ();

    # Publish this plugin's diagnostics for bufId, computed against version
    # (see bufferInfo). Rejected silently if version is stale.
    publishDiagnostics @23 (bufId: UInt32, version: UInt64, diagnostics: List(PluginDiagnostic)) -> ();

    # Path-keyed sibling of publishDiagnostics for files with no open buffer
    # (no bufId/version to check). Call from an OnWorkspaceScan handler.
    publishWorkspaceDiagnostics @24 (path: Text, diagnostics: List(PluginDiagnostic)) -> ();
    # Register a handler invoked on a workspace-wide diagnostic scan
    # (server startup, or an explicit rescan).
    registerWorkspaceScanHandler @25 (handler: WorkspaceScanHandler) -> ();
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
    getFixes       @1 (fixData: Text) -> (items: List(FixItem));
    applyFix       @2 (fixData: Text, index: UInt32) -> ();
}
interface ActionProvider {
    getActions  @0 (bufId: UInt32, line: UInt32, col: UInt32) -> (items: List(ActionItem));
    applyAction @1 (bufId: UInt32, line: UInt32, col: UInt32, index: UInt32) -> ();
}
interface CompletionProvider {
    getCompletions    @0 (bufId: UInt32, line: UInt32, col: UInt32) -> (items: List(CompletionItem));
    resolveCompletion @1 (item: CompletionItem) -> (item: CompletionItem);
}
interface PopupHandler {
    selected  @0 (data: Text) -> ();
    cancelled @1 () -> ();
}
interface InputPromptHandler {
    confirmed @0 (text: Text) -> ();
    cancelled @1 () -> ();
}
interface EditEventHandler {
    linesChanged @0 (bufId: UInt32, filePath: Text, atLine: UInt32, lineDelta: Int32) -> ();
}
interface WorkspaceScanHandler {
    scan @0 () -> ();
}
struct PluginDiagnostic {
    range    @0 :PluginRange;
    severity @1 :PluginDiagnosticSeverity;  # error | warning | info | hint
    message  @2 :Text;
    # No source field — the server stamps the publishing plugin's name on
    # as Source when merging with LSP/lint diagnostics.
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
name    = "blame"
version = "1.0.0"
description = "Show git blame for the current line"

# Optional: if the plugin registers exactly one key binding, trigger_key
# overrides which physical key it's bound to; key_description is shown for
# it in the ? help popup (falls back to description, truncated).
trigger_key     = "ctrl+g"
key_description = "Show git blame for current line"

[binaries]
"darwin/arm64" = "blame-darwin-arm64"
"darwin/amd64" = "blame-darwin-amd64"
"linux/amd64"  = "blame-linux-amd64"
"linux/arm64"  = "blame-linux-arm64"
```

The plugin manager selects the correct binary for the current `GOOS/GOARCH` at startup. See [Command-menu contributions](#command-menu-contributions) below for the `menu_item` manifest syntax.

A future `io plugin install <source>` command would automate downloading and unpacking plugin releases.

## Command-menu contributions

Pressing `space` in normal mode opens the **Command menu** — a popup list of actions, analogous to the built-in `g` (Go) menu. Plugins contribute entries to it declaratively, without the editor knowing about them ahead of time.

A plugin adds one or more `[[menu_item]]` tables to its `plugin.toml`:

```toml
[[menu_item]]
label   = "Jumpy"
command = "jumpy.start"
key     = "j"   # selector shown/typed within the Command menu; optional
```

`command` is an opaque id chosen by the plugin. At runtime the plugin registers a handler for that id via `OnMenuAction`, which has the same signature as `OnKey` — it receives a `KeyContext` (`ctx.Mode` is always `"normal"`) and returns a `KeyResponse`:

```go
api.OnMenuAction("jumpy.start", func(key string, ctx sdk.KeyContext) sdk.KeyResponse {
    // same handler shape as OnKey; a KeyResponse with CaptureKeys > 0 still
    // enters capture mode for subsequent physical keystrokes, e.g. jumpy's
    // 2-character jump labels.
    return sdk.KeyResponse{Handled: true, CaptureKeys: 2}
})
```

Unlike `OnKey`, a menu action is **never** bound to a physical key — it's reachable only by selecting it from the Command menu. This is the key difference from `registerKeyBinding` + `trigger_key`: the latter claims a keystroke; `registerMenuAction` only claims a menu slot.

A plugin with multiple actions can group them into a submenu by nesting `[[menu_item.children]]`:

```toml
[[menu_item]]
label = "Git"
key   = "g"

  [[menu_item.children]]
  label   = "Blame current line"
  command = "git.blame"
  key     = "b"

  [[menu_item.children]]
  label   = "Show log"
  command = "git.log"
  key     = "l"
```

A group node (has `children`, no `command`) opens a submenu when selected; a leaf node (has `command`, no `children`) invokes the registered handler directly. The `jumpy` and `bookmarks` plugins in `plugins/` are the reference examples — both ship as flat single-item entries, converted from what used to be physical key bindings (`J` and `alt+b` respectively).

## Plugin lifecycle

1. Server starts; reads `~/.config/indigo/plugins/`
2. For each valid plugin, spawns the binary: `./jumpy-darwin-arm64 --socket /tmp/indigo-<workspace>/plugin-jumpy.sock`
3. Plugin connects to the socket and calls `Plugin.initialize(api)`
4. Plugin registers handlers; server stores capability references
5. Server routes events to relevant plugins throughout the session
6. On server shutdown (all clients disconnected), sends shutdown signal; plugins have 2s to exit cleanly before `SIGKILL`

## Document model

Plugins access buffer content through the Cap'n Proto document query API — they never touch the rope or gap buffer directly. The server-side `Buffer` type exposes the following operations to the plugin layer:

| Query                          | Description                                                  |
|--------------------------------|--------------------------------------------------------------|
| `readBuffer(bufId)`            | Full text of the buffer                                      |
| `readLines(bufId, start, end)` | Text of a line range (efficient for large files)             |
| `readRange(bufId, from, to)`   | Text of an arbitrary (line, col) → (line, col) range         |
| `wordAt(bufId, pos)`           | Start and end position of the word at a cursor position      |
| `bufferInfo(bufId)`            | Path, language ID, line count, dirty flag, buffer version    |
| `visibleRange(clientId)`       | First and last visible line in a connected client's viewport |

`wordAt` and `readRange` require two small additions to `internal/document/buffer.go` (`TextRange` and `WordAt` methods). The internal machinery (`logicalSlice`, `logicalOffset`) is already present; these are thin wrappers over it.

## Client/server boundary

**All plugins are server-side.** There is one plugin system, one SDK, one socket per plugin.

The apparent problem with this — that jumpy-style plugins need to know what's *visible* on screen, which is client state — is solved by **viewport synchronization**: the client sends `{topLine, height, width}` to the server whenever scroll position or window dimensions change. The server stores this per connected client. Plugins then call `visibleRange(clientId)` to find out which lines are on screen.

The data flow for an overlay plugin like jumpy:

```text
1. space, then j (Command menu → "Jumpy") → server → jumpy plugin's menu action
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
- **Event bus vs capability pattern**: the design above uses the capability pattern. An alternative is a pub/sub event bus where plugins subscribe to named event topics and the server fans out. This is simpler to implement but loses the type safety and backpressure properties of the capability approach.
