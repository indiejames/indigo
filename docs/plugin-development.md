# Writing a Plugin

A hands-on tutorial for building an indigo plugin from scratch. For the design rationale
(process model, Cap'n Proto, the capability pattern, timeouts) see
[plugin-architecture.md](plugin-architecture.md) — this document is deliberately just the
practical "how", not the "why".

For the full SDK API reference, see the **[SDK reference](#sdk-reference)** section at the
end — it's not duplicated here as a separate file; see that section for why.

## What you'll build

A small plugin that jumps to the next `TODO`/`FIXME` comment in the buffer, marks those
lines in the gutter, and runs from an entry in the Command (space) menu. It touches the
three things almost every plugin needs: a menu action, a decoration provider, and reading
buffer content.

## 1. Set up the project

A plugin is just a `package main` that imports the SDK and calls `sdk.Run`. It doesn't need
to live inside the indigo repo.

```
mkdir todo-plugin && cd todo-plugin
go mod init example.com/todo-plugin
go get github.com/indiejames/indigo/sdk
```

The SDK is its own Go module, `github.com/indiejames/indigo/sdk`, versioned with semver
independently of the editor and tagged `sdk/vX.Y.Z` in the indigo repo. Its only dependency
is Cap'n Proto, so none of the editor's own heavy dependencies (tree-sitter grammars, the
TUI framework, etc.) enter your plugin's module graph. The SDK version is what tells you
whether your plugin still compiles; while it is `0.x`, a minor bump may break the API.

## 2. The minimal plugin

Every plugin implements one interface:

```go
type Plugin interface {
    Init(api *sdk.Api) Info
}
```

`Init` is called once, right after the plugin connects to the editor; it's where you
register the handlers you care about and return `Info{Name, Version}`. Nothing else in the
`Plugin` interface exists — you only implement handlers for events you actually register
for.

```go
// main.go
package main

import "github.com/indiejames/indigo/sdk"

type TodoPlugin struct {
    api *sdk.Api
}

func (p *TodoPlugin) Init(api *sdk.Api) sdk.Info {
    p.api = api
    return sdk.Info{Name: "todo", Version: "0.1.0"}
}

func main() {
    if err := sdk.Run(&TodoPlugin{}); err != nil {
        panic(err)
    }
}
```

`sdk.Run` blocks for the lifetime of the plugin process — it reads `--socket <path>` (passed
by indigo's plugin manager, not something you construct yourself), connects, and serves
until the editor disconnects.

This compiles and would load successfully, but doesn't do anything yet — nothing is
registered in `Init`.

## 3. Add a Command-menu action

A plugin's actions run from the Command menu, which opens when you press `space` in normal
mode. Register what an action does with `Api.OnMenuAction`, under an opaque id of your
choosing. The handler receives a `KeyContext` (buffer ID, mode, client ID, cursor position)
and returns a `KeyResponse` saying what happened.

```go
func (p *TodoPlugin) Init(api *sdk.Api) sdk.Info {
    p.api = api
    api.OnMenuAction("todo.jump", p.jumpToNext) //nolint:errcheck
    return sdk.Info{Name: "todo", Version: "0.1.0"}
}

// jumpToNext moves the cursor to the next TODO/FIXME after the current line.
func (p *TodoPlugin) jumpToNext(key string, ctx sdk.KeyContext) sdk.KeyResponse {
    // ReadBuffer is used in this example for simplicity, but a real plugin would
    // probably use ReadLines or ReadRange to avoid holding the entire document
    // in memory.
    content, err := p.api.ReadBuffer(ctx.BufID)
    if err != nil {
        return sdk.KeyResponse{}
    }
    lines := strings.Split(content, "\n")
    for i := int(ctx.CursorLine) + 1; i < len(lines); i++ {
        if strings.Contains(lines[i], "TODO") || strings.Contains(lines[i], "FIXME") {
            p.api.ShowMessageTo(ctx.ClientID, "Jumped to TODO") //nolint:errcheck
            return sdk.KeyResponse{Handled: true, HasCursor: true, CursorLine: uint32(i)}
        }
    }
    p.api.ShowMessageTo(ctx.ClientID, "No more TODOs") //nolint:errcheck
    return sdk.KeyResponse{Handled: true}
}
```

The menu entry itself, with its label and the key that picks it, is declared in
`plugin.toml` (step 6), not in Go code. `OnMenuAction` only wires up what happens when it's
selected, and `ctx.Mode` is always `"normal"` there.

`OnMenuAction`/`OnInsert`/`OnCommand` all return an `error` from the registration RPC call
itself (not from your handler) — it only fails if the connection to the editor is already
broken, at which point nothing else works either, so the bundled plugins in this repo
consistently ignore it with `//nolint:errcheck`. This tutorial follows that convention.

A handler has a **300ms deadline** — if it doesn't respond in time, the action does
nothing. Don't do slow I/O (network calls, etc.) directly in a handler.

## 4. Keys: what a plugin can and can't do

**A plugin cannot bind a key.** Every plugin action is reached through the Command menu,
under the key its `plugin.toml` gives it, so a plugin can never take over a key the editor
uses, two plugins can never fight over one key, and the user finds every plugin action in
one place. (SDK 0.1 had `OnKey`; it was removed in 0.2.0, and an editor from 0.2.0 on
refuses the binding.) Two things that look like key handling are still allowed:

- **Reading the keys after your own action.** Return `CaptureKeys: n` from the handler and
  the next `n` keystrokes go to it as well, with `ctx.Mode == "capture"`. That's how the
  bundled `jumpy` plugin reads the two-letter label of the place to jump to. Esc ends a
  capture, and is passed to the handler too, so it can clean up.
- **Being told about typed characters.** `OnInsert(char, fn)` runs `fn` when `char` is typed
  in insert mode. The character is always inserted first and `fn` runs in the background,
  so a slow plugin can delay its own response but never the keystroke. An inline-completion
  plugin uses this.

## 5. Add a decoration provider

Decorations annotate lines in the gutter, as inline overlays, or in the status bar.
`Api.Decorations` registers a function the editor calls whenever it needs to redraw the
currently-visible range — it should be fast and side-effect-free:

```go
func (p *TodoPlugin) Init(api *sdk.Api) sdk.Info {
    p.api = api
    api.OnMenuAction("todo.jump", p.jumpToNext) //nolint:errcheck
    api.Decorations(p.decorate)                 //nolint:errcheck
    return sdk.Info{Name: "todo", Version: "0.1.0"}
}

// decorate marks TODO/FIXME lines in the gutter, for whatever's currently visible.
func (p *TodoPlugin) decorate(bufID uint32, clientID uint64, r sdk.Range) []sdk.Decoration {
    lines, err := p.api.ReadLines(bufID, r.Start.Line, r.End.Line+1)
    if err != nil {
        return nil
    }
    var decs []sdk.Decoration
    for i, line := range lines {
        if strings.Contains(line, "TODO") || strings.Contains(line, "FIXME") {
            decs = append(decs, sdk.Decoration{
                Line:      r.Start.Line + uint32(i),
                Text:      "!",
                Kind:      sdk.DecorationGutter,
                TextColor: "#FFAA00",
            })
        }
    }
    return decs
}
```

Only read the visible range (`r`, given to you) rather than the whole buffer — decorations
are recomputed on every scroll/viewport change, so this runs often. `ReadLines` is the cheap
way to do that; `ReadBuffer` (used in `jumpToNext`) reads the whole file and is fine for an
occasional action but wasteful to call from a decoration provider.

> Contributing to the completion popup (`api.Completions`/`api.CompletionsFull`) follows the
> same shape as `Decorations` above — a fast callback the editor calls on every request,
> plus an optional `ResolveCompletion` callback for anything too slow to run on every
> keystroke (e.g. an npm registry lookup for package.json version completions). See
> [plugin-architecture.md](plugin-architecture.md#what-plugins-can-do) and the `CompletionHandlers`
> doc comment in `sdk/sdk.go` for the full shape.

## 6. Write `plugin.toml`

```toml
name        = "todo"
version     = "0.1.0"
sdk_version = "0.2.0"
description = "Jump to and highlight TODO/FIXME comments"

[binaries]
"darwin/arm64" = "todo-darwin-arm64"
"darwin/amd64" = "todo-darwin-amd64"
"linux/amd64"  = "todo-linux-amd64"
"linux/arm64"  = "todo-linux-arm64"

[[menu_item]]
label   = "Jump to next TODO"
command = "todo.jump"
key     = "t"
```

`sdk_version` is the SDK version your plugin was built against: the version of
`github.com/indiejames/indigo/sdk` in your `go.mod`, also available as `sdk.Version`. indigo
checks it before starting the plugin and refuses one it can't run, logging why. The major
versions must match, and while the SDK is `0.x` the minor versions must match too. From 1.0
on, indigo runs plugins built against its own minor version or any older one. Patch versions
never matter. A manifest without `sdk_version` still loads, with a warning in the log, but
the field is how a mismatch shows up as a clear message instead of a mysterious failure.

`[binaries]` maps `GOOS/GOARCH` to a binary filename in the same directory — the plugin
manager picks the right one for the machine it's running on at startup. You only need to
build and list the platforms you actually intend to support (usually just your own, while
developing). `[[menu_item]]`'s `command` must match the id you passed to `OnMenuAction`;
`key` is the shown/typed selector within the menu, and is optional. See
[plugin-architecture.md § Command-menu contributions](plugin-architecture.md#command-menu-contributions)
for submenus (nested `[[menu_item.children]]`), and
[security.md § Plugin binary integrity](security.md#plugin-binary-integrity) if you want to
add a `[hashes]` table so indigo verifies the binary before running it.

## 7. Build and install

```
GOOS=darwin GOARCH=arm64 go build -o todo-darwin-arm64 .
# ...repeat per platform in [binaries], or just build for your own machine while developing

mkdir -p ~/.config/indigo/plugins/todo
cp todo-darwin-arm64 plugin.toml ~/.config/indigo/plugins/todo/
```

Plugins are discovered once, when the indigo **server** starts for a workspace — not per
client window. If a server is already running (it survives after you quit an `indigo`
window, until the last connected client disconnects), a newly installed plugin won't be
picked up until that server exits. While iterating, the reliable way to force a reload is to
quit every indigo window for the workspace, then reopen one.

## 8. Try it out

Open a file containing a `TODO` comment, then:

- Press `space`, then the key you gave `[[menu_item]]` (`t` above) — the cursor jumps to
  the next `TODO`/`FIXME` below it, and the status bar confirms.
- Lines containing `TODO`/`FIXME` show a `!` in the gutter.

## Debugging

A plugin's stdin/stdout are closed by the plugin manager, but **stderr is redirected into
the day's log file in `$TMPDIR`** — `indigo-plugins-<YYYY-MM-DD>.log`, shared with indigo's
own internal server/client logs. The log rotates daily and files untouched for 24h are
deleted (see `internal/debuglog`); set `INDIGO_LOG_DIR` to put them somewhere else. Note
that a plugin process keeps whichever file existed when it was spawned, so a plugin started
before midnight keeps logging to the previous day's file. While developing, just:

```
fmt.Fprintln(os.Stderr, "todo: decorate called, bufID=", bufID)
```

and:

```
tail -f "${INDIGO_LOG_DIR:-${TMPDIR:-/tmp}}/indigo-plugins-$(date +%Y-%m-%d).log"
```

(the same directory Go's `os.TempDir()` resolves to — `$TMPDIR` if set, `/tmp`
otherwise — unless `INDIGO_LOG_DIR` overrides it.)

## SDK reference

The full SDK API — every method on `Api`, every type, and package-level usage notes — is
documented directly in Go doc comments on `github.com/indiejames/indigo/sdk`
(`sdk/sdk.go` and friends), viewable with:

```
go doc github.com/indiejames/indigo/sdk
go doc github.com/indiejames/indigo/sdk.Api      # just the Api type's methods
```

or by reading `sdk/sdk.go` directly — every exported type and method has a doc comment.
This is intentionally **not** duplicated into a separate markdown file: a hand-maintained
API reference next to a real one drifts out of sync the first time either changes without
the other. The godoc comments are the source of truth; this tutorial and
[plugin-architecture.md](plugin-architecture.md) cover everything else (the process model,
protocol, distribution format, and a worked example) that godoc isn't the right place for.

## Full example

```go
package main

import (
    "strings"

    "github.com/indiejames/indigo/sdk"
)

type TodoPlugin struct {
    api *sdk.Api
}

func (p *TodoPlugin) Init(api *sdk.Api) sdk.Info {
    p.api = api
    api.OnMenuAction("todo.jump", p.jumpToNext) //nolint:errcheck
    api.Decorations(p.decorate)                 //nolint:errcheck
    return sdk.Info{Name: "todo", Version: "0.1.0"}
}

func (p *TodoPlugin) decorate(bufID uint32, clientID uint64, r sdk.Range) []sdk.Decoration {
    lines, err := p.api.ReadLines(bufID, r.Start.Line, r.End.Line+1)
    if err != nil {
        return nil
    }
    var decs []sdk.Decoration
    for i, line := range lines {
        if strings.Contains(line, "TODO") || strings.Contains(line, "FIXME") {
            decs = append(decs, sdk.Decoration{
                Line:      r.Start.Line + uint32(i),
                Text:      "!",
                Kind:      sdk.DecorationGutter,
                TextColor: "#FFAA00",
            })
        }
    }
    return decs
}

func (p *TodoPlugin) jumpToNext(key string, ctx sdk.KeyContext) sdk.KeyResponse {
    content, err := p.api.ReadBuffer(ctx.BufID)
    if err != nil {
        return sdk.KeyResponse{}
    }
    lines := strings.Split(content, "\n")
    for i := int(ctx.CursorLine) + 1; i < len(lines); i++ {
        if strings.Contains(lines[i], "TODO") || strings.Contains(lines[i], "FIXME") {
            p.api.ShowMessageTo(ctx.ClientID, "Jumped to TODO") //nolint:errcheck
            return sdk.KeyResponse{Handled: true, HasCursor: true, CursorLine: uint32(i)}
        }
    }
    p.api.ShowMessageTo(ctx.ClientID, "No more TODOs") //nolint:errcheck
    return sdk.KeyResponse{Handled: true}
}

func main() {
    if err := sdk.Run(&TodoPlugin{}); err != nil {
        panic(err)
    }
}
```
