# Debugging

indigo debugs programs through the [Debug Adapter Protocol](https://microsoft.github.io/debug-adapter-protocol/):
breakpoints in the gutter, the stopped line marked in the editor, stepping from
the keyboard, and a separate window (`indigo --debug`) for the call stack,
variables, watches and program output.

Go works out of the box with [Delve](https://github.com/go-delve/delve).
TypeScript and JavaScript (VS Code's js-debug), Python (debugpy) and C, C++,
Rust or Swift (lldb-dap) are built in too, and any other DAP debugger can be
added in the config.

Like language servers, the debugger belongs to indigo's **server**: one session
per workspace, shared by every window on it. Breakpoints set in one window show
in all of them, and move with their line as you edit.

## Quick start (Go)

1. Install Delve: `go install github.com/go-delve/delve/cmd/dlv@latest`.
2. Put the cursor on a line and press **F9** (or `Space d b`) — a `●` appears
   in the gutter.
3. Press **F5**. indigo builds and runs the current file's package, or its
   tests in a `_test.go` file, and stops at the breakpoint: `▶` marks the
   line, which is tinted, and the status bar shows `DEBUG breakpoint`.
4. **F10** steps over, **F11** steps in, **Shift+F11** steps out, **F5**
   continues, **Shift+F5** stops.
5. **K** on an expression while stopped shows its value — the identifier under
   the cursor with its selector chain (`cfg.Name`, not just `Name`), or the
   selection.

To debug just one test, put the cursor anywhere inside it and press
**Space d t**.

On **macOS**, the debugger needs Developer Mode: run
`sudo DevToolsSecurity -enable` once. Without it, a launch waits on an
authorization prompt that never appears in a terminal, and indigo reports that
after the launch times out.

## Keys

In any editor window:

| Key | Action |
| --- | --- |
| F9 / `Space d b` | Toggle a breakpoint on the cursor's line |
| `Space d B` | Set the breakpoint's condition (creating the breakpoint if needed) |
| `Space d L` | Make it a logpoint: print a message instead of stopping |
| F5 | Continue; with no session running, start the last configuration again (the current package or file, the first time) |
| `Space d d` | Debug the current file's package (Go), or the file itself (other languages) |
| `Space d t` | Debug the Go test, benchmark, fuzz test or example the cursor is in |
| `Space d l` | Choose a [named configuration](#named-configurations) |
| `Space d r` | Restart: stop the session and start the last configuration again |
| F10 / `Space d n` | Step over |
| F11 / `Space d i` | Step in |
| Shift+F11 / `Space d o` | Step out |
| `Space d p` | Pause |
| Shift+F5 / `Space d x` | Stop |
| K | While stopped: evaluate the expression at the cursor |

Gutter marks: `●` a breakpoint, `◉` a conditional breakpoint, `◆` a logpoint,
and `▶` where the program is stopped. A hollow mark (`○` `◎` `◇`) is one the
running program could not set — no code on that line, a file that is not part
of the build, or a debugger that does not support conditions or logpoints —
and the reason is shown after the line.

## Conditional breakpoints and logpoints

**Space d B** asks for a condition, an expression in the program's own
language: `i > 10`, `name == "alice" && len(items) > 0`. The program then stops
there only when it is true. The condition is shown, dimmed, after the line. To
edit it, press Space d B again (the prompt is pre-filled); to go back to
always stopping, clear it. F9 removes the breakpoint, condition and all.

**Space d L** turns the breakpoint into a logpoint: instead of stopping, the
debugger prints the message to the program's output (the Output section of
`indigo --debug`) and carries on. Expressions in braces are evaluated:
`i={i} total={total}`. A logpoint can have a condition too, so it prints only
when that holds. Clearing the message makes it an ordinary breakpoint again.

Both are sent to the running session straight away, so they can be added or
changed while debugging. A debugger that does not support one is not sent
it — a conditional breakpoint would otherwise stop every time, and a logpoint
would stop instead of printing — and the mark goes hollow with the reason.
Delve, debugpy and lldb-dap support both.

Only the window you used last jumps to a stop. Other windows show the `▶` if
they have that file open, and stay where they are.

## The debug window

Run `indigo --debug` in another terminal or pane, in the project (with
`--devcontainer` or `--container` for a container, as for the editor). It
attaches to the workspace's session, including one already running.

| Section | Shows |
| --- | --- |
| Call Stack | The stopped thread's frames. **Enter** selects one: its variables and watches are shown, and the editor windows move to its source line. |
| Variables | The selected frame's scopes as a tree. Locals open by themselves; globals never do, as they can be slow to fetch. **Enter** toggles, **→** opens, **←** closes or moves to the parent. What you open stays open across steps. |
| Watch | Expressions evaluated at every stop, in the selected frame. **a** adds one, **d** deletes one. |
| Output | The program's output. It follows the end until you scroll up; **G** follows again. |

**Tab** / **Shift+Tab** switch sections, **↑↓** or **j k** move. Execution:
**c** continue, **n** step over, **i** step in, **o** step out, **p** pause,
**x** stop, **R** restart — or the same F-keys as the editor. **q** quits.

## Named configurations

For program arguments, environment variables, build flags, or a package other
than the current file's, define named configurations and pick one with
**Space d l**. They are read from three places, and an earlier one wins a name
clash:

1. `.indigo/debug.toml` in the workspace — commit it with the project.
2. `.vscode/launch.json` — see [below](#vs-code-launchjson).
3. `[[debug]]` entries in your `~/.config/indigo/config.toml`.

The workspace files are read each time the menu opens, so edits show up
without restarting anything.

```toml
# .indigo/debug.toml
[[debug]]
name = "server"
program = "./cmd/server"          # default: the workspace root
args = ["--port", "8080"]
env = { LOG_LEVEL = "debug" }
build_flags = "-tags dev"

[[debug]]
name = "store tests"
program = "./internal/store"
mode = "test"                     # Go: "debug" (default) or "test"
args = ["-test.run", "^TestStore", "-test.v"]

[[debug]]
name = "import script"
program = "scripts/import.py"     # a .py file: debugged with debugpy
args = ["--dry-run"]
launch = { justMyCode = false }
```

| Key | Meaning |
| --- | --- |
| `name` | Required; shown in the menu. |
| `adapter` | `go`, `python`, `lldb`, or a [`[[debug_adapter]]`](#other-debuggers) name. Left out, it is chosen by the program's file type — Go unless another adapter claims the extension. |
| `program` | The package directory (Go), script, or binary. Relative to the workspace root. |
| `mode` | Go only: `debug` runs a main package, `test` runs a package's tests. |
| `args` | Arguments for the program. For `go test`, use `-test.run`, `-test.v`, and so on. |
| `cwd` | Working directory; default the workspace root. |
| `env` | Added to the program's environment. |
| `build_flags` | Go only: passed to the build, e.g. `-tags dev`. |
| `launch` | Anything else the debugger's launch request takes, passed as is and over indigo's own settings: Delve's `dlvFlags`, debugpy's `justMyCode`, lldb-dap's `initCommands`, … |

`${workspaceFolder}` in `program`, `cwd` and `args` is replaced by the
workspace root.

Once you have started a configuration, **F5** (with no session running) and
**Space d r** start it again, so the usual loop is: pick it once, then F5 after
each fix. That includes a launch that failed to build.

## VS Code `launch.json`

If the project has a `.vscode/launch.json`, its `launch` entries appear in the
**Space d l** menu without any change. Comments and trailing commas are fine.

- Supported types: `go`, `node`/`pwa-node`, `debugpy`/`python`,
  `lldb-dap`/`lldb`, and any `[[debug_adapter]]` whose name matches the
  entry's `type`. Other types (Chrome, …) and `attach` requests are skipped.
- Variables: `${workspaceFolder}`, `${workspaceFolderBasename}`, `${file}`,
  `${fileDirname}`, `${fileBasename}`, `${fileBasenameNoExtension}`,
  `${relativeFile}`, `${relativeFileDirname}`, `${env:NAME}`. The `${file}`
  family means the file open in the window you pressed Space d l in. An entry
  using anything else (`${input:…}`, `${command:…}`) is reported and left out.
- Go's `"mode": "auto"` becomes `test` for a `_test.go` program and `debug`
  otherwise, as in vscode-go.
- `console`, `preLaunchTask`, `postDebugTask`, `presentation` and `envFile`
  are ignored. `console` has to be: debugpy refuses to launch with
  `"integratedTerminal"` unless the editor can open terminals for it, which
  indigo does not. Left out, it uses its own console, whose output indigo
  shows.

## Other debuggers

Built in, used when their command is found:

| Adapter | Command | Debugs |
| --- | --- | --- |
| `go` | `dlv` (Delve) | Go packages and tests |
| `node` | js-debug — see [TypeScript and JavaScript](#typescript-and-javascript) | `.ts`, `.js`, `.mjs`, `.cjs`, `.mts`, `.cts` files |
| `python` | `python3 -m debugpy.adapter` | `.py` files — install with `pip install debugpy` |
| `lldb` | `lldb-dap` (on macOS, found through Xcode's `xcrun` when not on `PATH`) | Built binaries: C, C++, Rust, Swift. Set `program` to the binary, compiled with debug info (`-g`). |

Space d d on a `.py` file debugs that file with debugpy, and on a `.ts` or `.js`
file runs it under Node with js-debug. lldb-dap has no
extension, because its program is a binary rather than the source file you have
open; give it a named configuration:

```toml
[[debug]]
name = "tool"
adapter = "lldb"
program = "./build/tool"
args = ["input.txt"]
```

Add or replace debuggers with `[[debug_adapter]]` in `config.toml`:

```toml
[[debug_adapter]]
name = "python"                        # replaces the built-in one
command = "/home/me/venvs/dev/bin/python"
args = ["-m", "debugpy.adapter"]
extensions = [".py"]
launch = { justMyCode = false }

[[debug_adapter]]
name = "netcoredbg"                    # C#; program is the built .dll
command = "netcoredbg"
args = ["--interpreter=vscode"]
```

| Key | Meaning |
| --- | --- |
| `name` | What a configuration's `adapter` refers to. |
| `command`, `args` | How to start it. |
| `transport` | `stdio` (default): DAP over the adapter's stdin/stdout. `tcp`: the adapter prints `listening at host:port` and indigo connects. |
| `extensions` | Source files Space d d debugs directly with this adapter. |
| `launch` | Defaults merged into every launch request to it; a configuration's own `launch` wins. |

indigo sends `program`, `cwd`, `args` and `env` as the launch request's fields
of those names, which most adapters use. Anything adapter-specific goes in
`launch`. An adapter that asks the editor for child sessions
(`startDebugging`, as js-debug does) is supported when it is reached over
`tcp`; one that asks the editor to open a terminal (`runInTerminal`) is not.

## TypeScript and JavaScript

indigo debugs Node programs with [js-debug](https://github.com/microsoft/vscode-js-debug),
the debugger inside VS Code, run on its own. Setting it up takes two things:

1. Node.js on your `PATH`.
2. js-debug's download, unpacked into `~/.local/share/indigo`. It is not on
   npm; take the `js-debug-dap-v….tar.gz` file from
   [its releases](https://github.com/microsoft/vscode-js-debug/releases):

   ```sh
   mkdir -p ~/.local/share/indigo
   curl -L https://github.com/microsoft/vscode-js-debug/releases/download/v1.140.0/js-debug-dap-v1.140.0.tar.gz \
     | tar -xz -C ~/.local/share/indigo
   ```

   This creates `~/.local/share/indigo/js-debug/src/dapDebugServer.js`, which
   is where indigo looks. There is nothing to install or configure beyond
   that.

To keep js-debug somewhere else, tell indigo where it is by replacing the
built-in `node` adapter in `config.toml`:

```toml
[[debug_adapter]]
name = "node"
command = "node"
args = ["/opt/js-debug/src/dapDebugServer.js", "0", "127.0.0.1"]
transport = "tcp"
adapter_id = "pwa-node"
extensions = [".js", ".mjs", ".cjs", ".ts", ".mts", ".cts"]
launch = { type = "pwa-node", skipFiles = ["<node_internals>/**"] }
```

(If a command named `js-debug-adapter` is on your `PATH`, indigo runs that
instead of looking in `~/.local/share/indigo`. Some editors' package managers
install one; you do not need it.)

Once js-debug is in place, **Space d d** on a `.ts` or `.js` file runs it, stopping at its breakpoints, and
everything else works as for Go: stepping, `K` to evaluate, conditional
breakpoints and logpoints, and the debug window.

**TypeScript** runs directly on Node 23.6 and later, which strip type
annotations themselves (22.6 and later need `"runtimeArgs":
["--experimental-strip-types"]`). That leaves the line numbers as they are, so
breakpoints need nothing more. For TypeScript that type stripping cannot
handle (`enum`, `namespace`, parameter properties), run it through
[tsx](https://tsx.is) instead, which js-debug follows through source maps:

```toml
[[debug]]
name = "server"
program = "src/server.ts"
launch = { runtimeExecutable = "tsx" }
```

For a project compiled with `tsc`, debug the output and let source maps lead
back to the `.ts` files: enable `sourceMap` in `tsconfig.json` and set
`program` to the compiled `.js`, adding `launch = { outFiles =
["${workspaceFolder}/dist/**/*.js"] }` if the output is not beside the
sources.

A project's `.vscode/launch.json` Node entries work as they are.

Under the hood js-debug starts a second debug session for the program itself
(a `startDebugging` request), and for each worker thread or child process
the program starts. indigo opens each one, and steps and inspects whichever
stopped last. Browser debugging (`pwa-chrome`) is not supported.

## In a dev container

The debugger runs next to the server, inside the container, so it has to be
installed there, and the container has to allow it to trace processes. The
devcontainers Go feature does both: it installs `dlv` and adds the `SYS_PTRACE`
capability and `seccomp=unconfined` that ptrace needs.

```jsonc
// .devcontainer/devcontainer.json
{ "features": { "ghcr.io/devcontainers/features/go:1": {} } }
```

Without the feature, install Delve yourself and add the permissions:

```jsonc
{
  "postCreateCommand": "go install github.com/go-delve/delve/cmd/dlv@latest",
  "capAdd": ["SYS_PTRACE"],
  "securityOpt": ["seccomp=unconfined"]
}
```

For a container you started yourself (`--container`), pass
`--cap-add=SYS_PTRACE --security-opt seccomp=unconfined` to `docker run`.
Without them, a launch fails with an error about `ptrace` or "operation not
permitted".

Other languages' debuggers go in the image the same way: `pip install
debugpy`, lldb-dap from the distribution's LLVM packages, or js-debug unpacked
into the container user's `~/.local/share/indigo` (a `postCreateCommand` with
the `curl … | tar` line above).

`.indigo/debug.toml` and `launch.json` are in the workspace, so they apply in
the container unchanged. `[[debug]]` and `[[debug_adapter]]` entries in
`config.toml` come from the container user's config, as for language servers.
Open the debug window with `indigo --devcontainer --debug`.

## Limitations

- One session per workspace at a time.
- Breakpoints live in the server and are not saved: they are gone once the
  last window on the workspace closes. They follow edits made in indigo, but
  not a change made outside it (a `git checkout`), which can leave one on the
  wrong line.
- No hit-count breakpoints ("stop on the 5th time"), and no attaching to a
  running process, yet.
- Selecting a frame in the debug window moves every editor window to it, not
  just the last one used.
