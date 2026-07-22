# Performance

This document covers startup-time investigations and the optimizations that came out of
them. It's meant as a reference for anyone (human or otherwise) touching the startup path
again later, so the reasoning doesn't have to be rediscovered from scratch.

## Startup, before

Opening `indigo` in a workspace for the first time (no server running yet) does, roughly:

1. Client execs, checks for a running server for this workspace, spawns one if needed
   (`cmd/indigo/main.go`).
2. The server starts each configured plugin (`internal/plugin/manager.go`) as a subprocess,
   over a Unix socket, then does a Cap'n Proto handshake and calls its `Initialize` RPC.
3. The client dials the server, then calls `GetPluginKeys`, `GetPluginBindings`, and
   `GetMenuItems` to pick up plugin-contributed keybindings and menu items before it renders
   anything.

Two distinct things made this slow, and only the first showed up in profiling — the second
had to be found by testing the actual OS behavior directly.

### 1. Plugin startup was sequential

`Manager.Start()` looped over discovered plugins one at a time: spawn, wait for its socket,
connect, `Initialize` RPC — then move to the next plugin. With N plugins that's the *sum* of
N startup times instead of the *max*. Measured with 5 plugins (bookmarks, hello, indigo-git,
indigo-spell, jumpy), each plugin's own startup was ~50-95ms, for a sequential total of
~310ms.

`startPlugin` only ever touched shared `Manager` state (`m.plugins`) under `m.mu`, and each
plugin's own `registeredPlugin` state under its own `reg.mu` — so it was already safe to call
concurrently; parallelizing it was a matter of wrapping the loop body in a goroutine with a
`sync.WaitGroup`, not adding new synchronization.

Separately, `GetPluginBindings`/`GetMenuItems` (the two client startup RPCs that need
plugins to have finished starting) were each awaited fully before the next was even issued.
Since both block on the same server-side readiness signal, issuing all three RPCs (including
`GetPluginKeys`, which doesn't) before awaiting any of them means that wait is paid once
instead of stacked.

Two polling loops (`waitForSocket` in the plugin manager, `waitForServer` in `cmd/indigo`)
also slept a fixed 50ms between checks. Since the check almost always succeeds well before
the first sleep elapses, each call was paying close to a full 50ms of pure waiting for
something that had usually already happened.

### 2. macOS's first-execution validation cost

This is the bigger one, and it's an OS behavior, not a bug in this codebase. The first time
a binary with new content is executed, macOS spends time validating it before the process's
own code starts running — confirmed by direct measurement:

```
$ go build -o freshbin ./plugins/hello
$ time ./freshbin --help    # first execution of this exact file
real  0m0.240s
$ time ./freshbin --help    # second execution, same file
real  0m0.003s
```

That cost is paid **per binary**, independently, the first time each one runs after its
content changes. After a full rebuild (the main `indigo` binary plus all 5 plugins), that's
up to 6 separate validation delays. Run sequentially — which they effectively were, given
finding (1) — this is what turned "rebuilt indigo" into a multi-second pause.

Two things worth being explicit about, since they were tested and ruled out rather than
assumed:

- **Ad-hoc code-signing does not help.** Signed and unsigned first-runs of the same fresh
  binary measured the same (~0.19-0.24s either way, within noise). This isn't classic
  Gatekeeper/quarantine (that applies to files carrying the `com.apple.quarantine` xattr,
  which locally-built binaries don't have) — it behaves like AMFI's local trust-cache
  validation, which runs on first execution of *changed content* regardless of an ad-hoc
  signature's presence.
- **A real Developer ID signature + notarization** might interact differently in principle,
  but requires a paid Apple Developer account and submitting every build to Apple's
  notarization service over the network (typically minutes of turnaround). That's
  incompatible with a local `go build && run` iteration loop and wasn't pursued.
- **Re-executing the same already-validated binary as a new process is free.** The
  server subprocess (spawned from the same file the client just executed) does not pay a
  second validation delay — only genuinely new/changed file content does.

## Fixes applied

| Fix | Where |
|---|---|
| Parallelize plugin startup (`sync.WaitGroup` instead of a sequential loop) | `internal/plugin/manager.go`, `Manager.Start` |
| Pipeline the three startup RPCs (issue all three, then await each) | `internal/client/rpc.go`, `Dial` |
| Tighten poll intervals from 50ms to 5ms | `internal/plugin/manager.go` (`waitForSocket`), `cmd/indigo/main.go` (`waitForServer`) |
| `--warm` flag: exits immediately, touching nothing | `sdk.Run` (covers every plugin built against the SDK), `cmd/indigo/main.go` |
| Run each binary once with `--warm` right after building it | `Makefile`: `install`, `install-hello`, `install-jumpy`, `install-spell`, `install-git`, `install-bookmarks` |

The `--warm` mechanism is the one that matters most: it doesn't eliminate the OS validation
cost (nothing can, short of not changing the binary), it just moves *when* it's paid — from
your next interactive `indigo` launch to the `make install` step, where a brief pause is
already expected.

If you build a plugin or `indigo` itself by hand instead of via the `Makefile` targets (e.g.
`go build -o ...`), that binary won't be pre-warmed, and its first launch afterward will pay
the validation cost interactively. Either run it once with `--warm` yourself, or just expect
that one launch to be slower.

## Measured results

All measurements are wall-clock time from process exec to the TUI's first rendered frame,
on the same machine, using otherwise-identical binaries.

| Scenario | Time |
|---|---|
| Cold: fresh rebuild, sequential plugin startup (original) | ~1.5-2s+ (extrapolated from per-plugin measurements) |
| Cold: fresh rebuild, parallelized plugin startup, **no** `--warm` | 1.082s |
| Fresh rebuild, `--warm` applied to every binary (i.e. after `make install`) | 0.165s |
| Server already running (the common case — every launch after the first in a session) | ~0.10s (0.099-0.114s across 5 trials) |

Net effect: a full rebuild's first launch drops from a couple of seconds to ~0.17s, and every
subsequent launch while the server is already up (the typical case while actively working)
lands around 0.10s regardless of how many plugins are installed.

## Non-goals / things that turned out not to matter

- **LSP server startup** (gopls, etc.) is already lazy — `lsp.Manager` only starts a
  language server the first time a matching file is opened (`clientForPath`), in a
  goroutine, and isn't on the editor-startup critical path.
- **The server subprocess's own exec** doesn't add a second validation delay on top of the
  client's, since it's the same already-validated file (see above).
