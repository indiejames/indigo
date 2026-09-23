# Dev containers

indigo can run its server inside a dev container while the editor itself stays
on your machine. Your terminal, theme, keybindings and clipboard are local; the
language servers, formatters and linters run in the container, against the
toolchain the project actually expects.

This is the same split VS Code's Dev Containers extension uses, and it maps onto
indigo's existing client/server architecture almost exactly — the server already
owns buffers, language servers and formatting, so it is the half that moves.

## Two ways in

### A container described by `devcontainer.json`

```sh
indigo --devcontainer .
```

indigo delegates to the [`devcontainer` CLI][cli] to build and start the
container, then connects to a server inside it. Features, docker-compose,
`postCreateCommand`, `remoteUser` and mounts all behave as the specification
says, because the CLI is the reference implementation of it.

Install the CLI first:

```sh
curl -fsSL https://raw.githubusercontent.com/devcontainers/cli/main/scripts/install.sh | sh
```

It bundles its own Node runtime. Note the script installs to `~/.devcontainers/bin`
and does **not** add that to your `PATH`; indigo looks there anyway, so there is
nothing further to do.

You also need a container engine — Docker, OrbStack, Colima or Podman. indigo
looks for one on `PATH` and in the usual install locations, and `INDIGO_DOCKER`
points it at one it cannot find.

**This is opt-in.** A repository that ships a `devcontainer.json` is not consent
to build and start a container every time you open a file in it, so indigo never
does this unless you ask. Without the flag, `indigo .` behaves exactly as before.

### A container you already have

```sh
indigo --container my-dev-box
indigo --container my-dev-box --container-dir /workspaces/proj
```

No `devcontainer.json` needed. `--container-dir` says where the workspace is
mounted inside the container; without it indigo assumes the same path as on the
host, which is right for `docker run -v $PWD:$PWD` and wrong for anything else.

## How the server gets there

The container needs an indigo server, and the one on your machine is the wrong
platform. indigo copies in a statically linked Linux binary — about 5 MB, built
by:

```sh
make build-container-server
```

`make install` puts both architectures in `~/.indigo/`. indigo also looks next
to its own executable, in `dist/`, and wherever `INDIGO_CONTAINER_SERVER`
points. The binary is copied into a container once and reused, and it needs no
changes to your image — it is statically linked, so glibc and musl images both
work (tested on Alpine).

If your image already ships one, say so in `devcontainer.json` and indigo will
use it instead of copying anything:

```jsonc
{
  "image": "mcr.microsoft.com/devcontainers/go:1",
  "customizations": {
    "indigo": {
      "serverPath": "/usr/local/bin/indigo-server"
    }
  }
}
```

`customizations` is an open namespace in the dev container specification —
`customizations.vscode` is the same mechanism — so this is a supported way for a
project to configure the editor rather than an extension of the format.

## Plugins

Plugins run inside the container, because that is where the server is. indigo
carries your installed plugins in automatically — but it can only carry a plugin
that has a build for the container's platform, and `make install-<plugin>` only
ever builds for your own.

Build the Linux ones once:

```sh
make build-plugins-linux
```

That puts linux/arm64 and linux/amd64 binaries beside the host ones in
`~/.config/indigo/plugins/`, which is what the plugin manifests have always
declared. A plugin with no matching build is skipped, and indigo says which on
startup rather than leaving you to notice.

A third-party plugin needs the same: a `linux/<arch>` entry in its `plugin.toml`
and the binary to go with it.

Nothing goes in `devcontainer.json` — plugins are yours, not the project's, and
indigo carries them across for you.

### git-backed plugins

indigo marks the workspace as a safe git directory inside the container. Without
that, git refuses to touch a bind-mounted repository owned by a different uid
(`detected dubious ownership`), every git command fails, and plugins that shell
out to git show nothing and explain nothing. VS Code's extension does the same
fixup; the `devcontainer` CLI does not.

The exception is scoped to your workspace, never `*`.

## Stopping the container

When the last indigo window closes, the container is stopped — the dev container
specification's default (`shutdownAction`), and what VS Code does. To keep it
running:

```jsonc
{ "shutdownAction": "none" }
```

`indigo --container` never stops anything: you started that container, so its
lifetime is yours.

With several windows open, only the last one out stops the container, and the
others exit immediately — each window can see the others directly rather than
waiting to find out.

Compose-based dev containers are left running, with a note on exit. Stopping
means bringing the whole project down, and stopping only indigo's own service
container would look like it worked while leaving the rest up.

## Multiple windows

Open as many indigo windows onto one container as you like: they share a single
server inside it, exactly as they do on your machine, so concurrent edits to the
same file converge. The server starts with the first window and exits with the
last.

`docker exec` is the transport, not the server — each window's exec is a bridge
to a socket inside the container. That is why no ports are published and nothing
needs configuring.

## An agent inside the container

If the container runs Claude Code (or another MCP client), point it at the
server indigo already put there, so the agent and your editor share buffers
and language servers:

```sh
# inside the container, as the same user indigo runs as (devcontainer.json's
# remoteUser), once
claude mcp add --scope user indigo -- /tmp/.indigo-server --mcp
```

Then start `claude` from inside the workspace. `/tmp/.indigo-server` is a link
indigo updates on every attach to the server build currently in use, so the
registration survives upgrades. It exists once an indigo window has attached to
the container at least once.

`--mcp` is the same MCP server as `indigo --mcp` (see
[agent-integration.md](agent-integration.md)); it just lives in the static
binary that is already in the container, so nothing has to be installed there.
It finds the server the same way — the nearest `.git` above the working
directory, the user id, and `TMPDIR` — so all three must match the editor's.
With no editor window attached it starts the server itself, as the `--daemon`
process a window would start, and a window attaching later joins it. That
server loads the plugins the most recent window carried in, through
`/tmp/.indigo-plugins` — so if the agent starts before any window has ever
attached to the container, it has none until every client has left and the
server restarts. It also does not get `remoteEnv` (see below).

To check it is sharing the editor's server, make one tool call and run
`ps -eo args | grep indigo` in the container: there should be exactly one
`--daemon` process for the workspace.

Do **not** use the host-side `indigo --mcp-http` recipe from agent-integration.md
for this: with the server in the container, there is no server on the host for
the workspace, so it would start a second one and the agent would edit a
separate copy of every file.

## Language servers, formatters and linters

These run inside the container, started by indigo's server there, so they have
to be installed in the image — nothing on your machine is used. The server looks
them up on its `PATH` (formatters and linters also in the project's
`node_modules/.bin`), and it uses the same built-in defaults as on your machine:
`gopls`, `rust-analyzer`, `typescript-language-server`, `pylsp`, `clangd`, … (see
[language-support.md](language-support.md)). Install one of those and it is
picked up with no configuration:

```jsonc
// .devcontainer/devcontainer.json
{
  "features": { "ghcr.io/devcontainers/features/go:1": {} },   // gopls
  "postCreateCommand": "npm i -g typescript typescript-language-server"
}
```

**Which `PATH`.** The server starts with the image's `ENV PATH` plus
devcontainer.json's `remoteEnv` — not what a login shell's `.bashrc`, nvm or
pyenv add. If a tool lands somewhere else, put it on `PATH` with `remoteEnv`:

```jsonc
"remoteEnv": { "PATH": "${containerEnv:PATH}:/home/vscode/.local/bin:/home/vscode/go/bin" }
```

`${containerEnv:NAME}` is resolved against the container's environment and
`${localEnv:NAME}` against your machine's. `remoteEnv` is applied when a window
starts the server, so after changing it close every window on the container and
reattach. A server started by an agent's `--mcp` with no window attached does
not get it. (`userEnvProbe` is not implemented: indigo does not run a login
shell to discover its `PATH`.)

**Overrides go in the container's config.** `[[language_server]]`,
`[[formatter]]`, `[[linter]]` and `format_on_save` are read by the server, so
they come from the
container user's `~/.config/indigo/config.toml`, not the one on your machine.
Ship it with the project, for example:

```jsonc
"postCreateCommand": "mkdir -p ~/.config/indigo && cp .devcontainer/indigo.toml ~/.config/indigo/config.toml"
```

An absolute `command = "/path/to/server"` there sidesteps `PATH` entirely.

**When one does not start**, its log is `/tmp/indigo-lsp-<command>.log` inside
the container; a missing binary shows up as `LookPath(...) failed` followed by
the `PATH` the server actually had.

## Paths

Attached to a container, indigo shows you the container's paths: a file is
`/workspaces/proj/main.go`, not `/Users/you/proj/main.go`. VS Code does the same,
and for the same reason — those are the paths the tools inside are working with.

The path you name on the command line is translated for you, so
`indigo --devcontainer ./cmd/main.go` does what you expect. A file *outside* the
workspace is refused rather than guessed at, because the container cannot see it.

Your recent-files list is keyed by where the workspace lives on your machine, so
two projects that both mount at `/workspaces/api` keep separate lists.

## What runs where

| | your machine | the container |
|---|---|---|
| rendering, keys, theme, undo, syntax highlighting | ✅ | |
| clipboard | ✅ | |
| recent files | ✅ | |
| `config.toml`: editor settings (keys, theme, display, `picker_ignore_dirs`) | ✅ | |
| `config.toml`: `[[language_server]]`, `[[formatter]]`, `[[linter]]`, `format_on_save`, recovery settings, and the indent settings passed to formatters | | ✅ — the container user's own `~/.config/indigo/config.toml` |
| buffers, editing, save | | ✅ |
| language servers, formatters, linters | | ✅ |
| file picker listing, workspace grep | | ✅ |
| plugins | | ✅ |

Because the file picker and workspace search run inside, the workspace does not
have to be bind-mounted — a named volume or a remote Docker host works too.
That matters on macOS, where Docker Desktop bind mounts are slow enough that
named-volume workspaces are a common workaround.

## Troubleshooting

**"the devcontainer CLI is not installed"** — install it with the script above,
or use `--container` against a container you start yourself.

**"no container runtime found"** — install Docker, OrbStack, Colima or Podman.
If one is installed somewhere indigo does not look, set `INDIGO_DOCKER` to its
full path.

**"no indigo-server-linux-… found"** — run `make build-container-server`.

**"… is outside the workspace …"** — the file is not mounted into the container.
If the workspace is mounted somewhere unexpected, pass `--container-dir`.

**Files are owned by root** — indigo runs the server as `devcontainer.json`'s
`remoteUser`. With `--container` there is no `devcontainer.json` to read one
from, so it runs as the image's default user.

Note that on Docker Desktop for macOS a bind-mounted workspace synthesizes
ownership: everything reports as `root` inside the container regardless of who
wrote it, and `chown` has no effect. That is the mount, not indigo.

**Everything is slow the first time** — `devcontainer up` may be pulling an
image, building it, and running lifecycle hooks. Later starts reuse the
container.

**`docker-credential-desktop: executable file not found`** — Docker's credential
helpers live beside the `docker` binary and are found through `PATH`. indigo
adds that directory for the processes it spawns, so this should not reach you;
if it does, add Docker's `bin` directory to your `PATH`.

Diagnostics go to the same shared log as everything else; see
`docs/agent-integration.md` for `get_logs`, and note that a wedged connection is
reported by the hang detector with `HANG` lines on both sides.

[cli]: https://github.com/devcontainers/cli
