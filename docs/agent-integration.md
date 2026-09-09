# Agent integration (MCP)

indigo can expose the workspace it already has open — live buffers, language
servers, formatters and linters — to a coding agent, over the [Model Context
Protocol](https://modelcontextprotocol.io). It is implemented in
`internal/agenttools` and runs as `indigo --mcp`.

The point is that the agent and you are looking at the *same* editor state. An
agent using ordinary shell tools reads what is on disk and searches text; through
this integration it reads your unsaved buffers and asks a real language server
where a symbol is used.

Written for Claude Code, which is what it is tested against. Any MCP client can
use it — the registration commands are the only Claude-specific part. Two
transports are available: **stdio** (`indigo --mcp`, the default, where the
client spawns the server) and **HTTP** (`indigo --mcp-http`, for a client that
cannot — an agent in a container, say). Both serve the same tools through the
same code; only the framing differs.

Supported MCP protocol revisions differ by transport. Over **stdio**: 2025-06-18,
2025-03-26 and 2024-11-05. Over **HTTP**: 2025-06-18 and 2025-03-26 only — the
HTTP endpoint implements Streamable HTTP, and 2024-11-05 predates it, using the
separate HTTP+SSE transport (a `/sse` endpoint plus a `/messages` endpoint)
which is not implemented. A client asking for a revision that is not supported
is answered with the newest one that is, rather than being refused.

## Setup

**1. Install indigo** so that `indigo` is on your `PATH`:

```sh
make install
```

The MCP server is the same binary as the editor. There is nothing separate to
install, and no plugin to enable.

**2. Register it with Claude Code**, once, at user scope:

```sh
claude mcp add --scope user indigo -- indigo --mcp
```

User scope is deliberate: one registration covers every repository. Claude Code
spawns `indigo --mcp` with the session's working directory, and indigo resolves
the workspace from there the same way the editor does — nearest ancestor with a
`.git`, symlinks resolved — so the agent reaches the same server your editor
would, from any subdirectory of any repo.

**3. Check it is connected**, from inside a session:

```
/mcp
```

`indigo` should be listed with its tools. If it is not, see
[Troubleshooting](#troubleshooting).

You do not need indigo open. If no server is running for the workspace, one is
started on demand and shuts down when nothing is using it. If you *do* have
indigo open, the agent shares that server, which is where the live-buffer
behaviour comes from.

## Running the agent in a container

The setup above has Claude Code *spawn* `indigo --mcp`, which ties the agent to
the same machine, filesystem and uid as the editor. An agent in a container has
none of those. Reaching the editor's Unix socket from one means aligning three
things at once — the socket path is
`$TMPDIR/indigo-<uid>-<sha256(abs workspace path)[:8]>/server.sock`, so the
workspace must be mounted at an identical path, the container must run as the
same uid, and the socket has to cross the boundary at all, which Docker Desktop
on macOS will not do for socket files.

Use the HTTP transport instead. On the host, beside the editor:

```sh
indigo --mcp-http            # 127.0.0.1:7391 by default
```

and register that URL from inside the container:

```sh
claude mcp add --transport http indigo http://host.docker.internal:7391
```

Nothing has to line up for it to *connect*: no `indigo` binary in the container,
no socket, no uid alignment. The workspace is resolved from the cwd of the
`--mcp-http` process on the host, so run it from the repository you want served.

Mount the repository at the **same absolute path** in the container anyway. Not
for connectivity — for legibility: tool results carry the host's absolute paths
(`find_references` answers with `/Volumes/.../thing.go:42`), and if the container
has that repo at `/workspace` those paths name nothing it can open, so the
agent's own Read and Bash cannot follow up on anything indigo tells it.

**Reaching it from the container.** Docker Desktop (macOS, Windows) forwards
`host.docker.internal` to the host, including services bound to loopback. On
Linux you need `--add-host=host.docker.internal:host-gateway`, and a
loopback-bound listener is *not* reachable that way — bind to the bridge address
or `0.0.0.0` and read the next paragraph first.

**Before binding beyond loopback.** This endpoint exposes your buffers and an
edit tool, and has no authentication of its own — the Unix socket's 0700
directory was doing that job. Binding wider hands anything that can route to the
port the ability to read and edit your files. Set a shared secret:

```sh
# on the host — generate it, keep it, then start the server with it
export INDIGO_MCP_TOKEN=$(openssl rand -hex 32)
echo "$INDIGO_MCP_TOKEN"          # you need this value again, in the container
indigo --mcp-http 0.0.0.0:7391
```

Every request then needs `Authorization: Bearer <token>`. The registration runs
in a *different* shell — inside the container — so the token has to be carried
across rather than referenced; paste the value printed above:

```sh
# in the container
export INDIGO_MCP_TOKEN=<the value printed on the host>
claude mcp add --transport http indigo http://host.docker.internal:7391 \
  --header "Authorization: Bearer $INDIGO_MCP_TOKEN"
```

indigo warns on stderr if you bind beyond loopback without a token. It is a
bearer token over plain HTTP, not real authentication — appropriate for a
container on the same host, not for anything routable.

Cross-origin requests are refused regardless: a browser can be coerced into
POSTing at a loopback port, and would attach an `Origin` naming the page. A real
MCP client is not a browser and sends none.

**If you would rather bridge the socket** — no HTTP listener, no token — it is
possible with `socat`, at the cost of aligning the uid and workspace path
described above:

Each side needs the socket path *that side* computes, and they differ: the
directory name is the same on both (it is `indigo-<uid>-<hash of the workspace
path>`, and you have aligned the uid and the path), but its parent is `$TMPDIR`,
which is `/var/folders/...` on macOS and `/tmp` in the container.

```sh
# host — derive the directory name, then point socat at the live socket
DIR="indigo-$(id -u)-$(printf %s /Volumes/Data/Golang/indigo | shasum -a 256 | cut -c1-16)"
socat TCP-LISTEN:7777,bind=127.0.0.1,reuseaddr,fork UNIX-CONNECT:"$TMPDIR/$DIR/server.sock"
```

```sh
# container, before starting claude — same directory name, container's TMPDIR
DIR="indigo-$(id -u)-$(printf %s /Volumes/Data/Golang/indigo | sha256sum | cut -c1-16)"
mkdir -p "/tmp/$DIR"
socat UNIX-LISTEN:"/tmp/$DIR/server.sock",fork,unlink-early TCP:host.docker.internal:7777
```

Substitute your own workspace path in both. If the two `DIR` values differ, the
container is computing a different workspace or uid and the bridge will not be
found — compare them before debugging anything else.

**The failure mode to watch for is quiet.** If the container cannot reach the
editor's server, `indigo --mcp` finds none running and starts its own inside the
container. Every tool then works perfectly against a second server with its own
buffers, and you get none of the shared-live-buffer behaviour you set this up
for. The tell is that reads never reflect your unsaved edits; `ps` inside the
container showing an `indigo --server` confirms it. The HTTP transport does not
have this failure mode, because there is nothing in the container to start.

## Optional: skip the approval prompts

Claude Code prompts before each tool call unless the tool is allowlisted. The
read-only tools are annotated as such and generally run without prompting; the
ones that change something do not. To stop being asked, add them to your
settings (`~/.claude/settings.json` for all projects, or a project's
`.claude/settings.local.json`):

```json
{
  "permissions": {
    "allow": [
      "mcp__indigo__apply_edits",
      "mcp__indigo__insert_at_line",
      "mcp__indigo__save_file"
    ]
  }
}
```

This is the only approval gate in the path. In MCP mode indigo does not show an
in-editor approval popup — `standaloneApprover` is `AlwaysApprove`, and approval
is deliberately left to the MCP client, the same model every other MCP server
uses. So allowlisting these means edits apply with no confirmation anywhere.
They are ordinary undoable buffer ops, so `u` in the editor reverses one.

## Optional but recommended: tell the agent to use it

Registering the tools makes them *available*; it does not make them *chosen*. An
agent has its own file-reading and searching tools, and will reach for the
familiar ones unless told otherwise — in practice this is the difference between
the integration working and appearing not to.

Put guidance in `~/.claude/CLAUDE.md` (applies everywhere) or a project's
`CLAUDE.md`. The section in this repo's own `CLAUDE.md` under "Code navigation"
is a working example to copy. The three things worth stating:

- **Which tool replaces which habit** — `find_references` instead of grepping
  for callers, `find_definition` instead of grepping for a definition,
  `list_symbols` instead of guessing which file holds something, `read_file`
  with `start_line`/`end_line` instead of `sed -n '95,140p'`,
  `get_diagnostics` instead of a build.
- **That a symbol name alone is a complete call.** `find_references`,
  `find_definition` and `list_symbols` all work from a bare name. An agent that
  believes it must find a file first will grep to find one — and that grep
  already answers the original question, so the tool never gets called.
- **That this outranks a general "work through the shell" instruction.** Some
  sessions carry one; Claude Code's auto mode ships an instruction naming
  `sed -n` and `grep` specifically. It is about preferring Bash over the
  built-in Read/Edit/Write tools, not a reason to answer a code question with
  grep. Worth saying explicitly, because otherwise the two silently compete.

## What the tools do

| tool | |
|---|---|
| `read_file` | File contents, or a line range via `start_line`/`end_line`. Reads the live buffer, so it includes unsaved edits. |
| `find_definition` | Where a symbol is defined. Takes a bare `symbol` name. |
| `find_references` | Every use of a symbol, with source-line previews. Takes a bare `symbol` name. |
| `list_symbols` | A file's outline (`path`), or a workspace-wide search (`query`). |
| `get_diagnostics` | Errors and warnings for one file, from its language server plus any linter, against the live buffer. |
| `get_workspace_diagnostics` | The same across the whole project. Pass `rescan=true` to pick up files that are not open. |
| `apply_edits` | Replace `old_text` with `new_text` in the live buffer, as an undoable op. |
| `insert_at_line` | Insert lines at an exact 1-based line number. |
| `save_file` | Write a buffer to disk. |
| `goto_file` | Move *your* editor window to a file and line. |

Directory listing and text search are deliberately **not** exposed: Claude
Code's own Glob and Grep already cover them, and disk-based search has no
buffer-consistency problem to solve. The language-server tools are the opposite
case — there is no native equivalent at all, and they are the main reason to
attach indigo to an agent session in the first place.

`read_file`, `get_diagnostics`, `get_workspace_diagnostics`, `find_definition`,
`find_references` and `list_symbols` are annotated `readOnlyHint`, which is what
lets a client run them without prompting. Everything that can change your code
is not, and keeps its prompt unless you allowlist it above.

Two properties are worth internalising, because everything surprising follows
from them:

- **Reads see the buffer, not the disk.** That is the advantage — an agent can
  reason about work you have not saved.
- **Edits land in the buffer, not the disk.** So the file stays stale until
  `save_file`. Anything reading from disk afterwards — a build, a test run,
  `git diff`, a grep — sees the old content until then. `apply_edits` and
  `save_file` both report the absolute path they wrote, and `save_file` reads
  the file back to confirm it matches the buffer, so a silent failure here is
  not possible.

## Troubleshooting

**Every result is prefixed with a warning about a stale server.** The server for
that workspace is running a binary that has since been replaced on disk —
usually because you ran `make install` while an editor window was open. It will
answer normally while silently lacking whatever you just built. Close every
indigo window on that workspace (or kill its `indigo --server` process) and
retry. indigo deliberately does not restart the server itself; see
`internal/server/staleness.go`.

**`read_file` says it read from disk rather than the buffer.** The server could
not be reached, so the content is whatever is on disk and any unsaved edits are
missing. Reads are degraded and edits will fail until it is fixed. Check that
`indigo` is on the `PATH` of the environment the agent runs in.

**The agent edits with its own tools instead of `apply_edits`.** Almost always
guidance rather than plumbing — see [the section
above](#optional-but-recommended-tell-the-agent-to-use-it). To tell the two
apart, ask it to call `apply_edits` explicitly: if that succeeds, the
integration is fine and the tool simply was not being chosen.

**A newly created file reports a parse error from a type-aware linter.** A file
that is not yet on disk is skipped by live linting for exactly this reason. A
file that *is* on disk but outside its `tsconfig.json`'s `include` really is
outside the project, and the error is a genuine finding about the project's
configuration — indigo is reporting it faithfully.

**Nothing works and `/mcp` shows the server as failed.** Run `indigo --mcp` by
hand in the repository. It should sit waiting on stdin rather than exiting; any
startup problem is printed to stderr.

## Removing it

```sh
claude mcp remove --scope user indigo
```

Nothing is left behind — no config in the repository, and no state outside the
indigo server that would have been running anyway.
