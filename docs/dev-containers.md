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
| your `config.toml`, recent files | ✅ | |
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
