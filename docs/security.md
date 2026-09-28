# Security Model

Indigo runs a local server process that manages open buffers and communicates
with editor clients and plugins over Unix domain sockets. This document
describes the threat model and the controls in place.

---

## Threat model

The primary concern is **a different user on the same machine** being able to:

- Connect a rogue client and read or modify your open files
- Register a rogue plugin that receives editor API access
- Intercept the connection between the server and a legitimate plugin

Indigo does not attempt to defend against a compromised process running as
**the same user** (that is a general OS-level problem that filesystem
permissions cannot solve).

---

## Unix socket security

### Client ↔ server socket

The server socket lives inside a private directory:

```
<tmp>/indigo-<uid>-<workspace-hash>/server.sock
```

`<tmp>` is Go's `os.TempDir()`: `$TMPDIR` when set — which on macOS is already a
per-user directory under `/var/folders/` — and `/tmp` otherwise, which is the
shared, world-writable case the rest of this document assumes. `<workspace-hash>`
is the first 8 bytes of the SHA-256 of the absolute workspace path.

The directory is created with mode `0700` before `net.Listen` is called.
Because the directory is owner-only, no other user can access the socket at
all — there is no window between socket creation and permission tightening.

The `<uid>` component ensures that two users whose workspace paths happen to
hash to the same value still get separate directories.

### Server ↔ plugin sockets

Plugin sockets live in the **same** `0700` directory as the server socket:

```
/tmp/indigo-<uid>-<workspace-hash>/plugin-<name>.sock
```

Because the directory is `0700`, a different user cannot place a socket there
to impersonate a plugin, and cannot read traffic between the server and a
legitimate plugin.

---

## Plugin binary integrity

Plugin manifests (`plugin.toml`) may include a `[hashes]` section with the
expected SHA-256 digest of each platform binary:

```toml
[binaries]
"darwin/arm64" = "jumpy-darwin-arm64"
"linux/amd64"  = "jumpy-linux-amd64"

[hashes]
"darwin/arm64" = "sha256:a3f8c1e2d4b076594f9b2e1a..."
"linux/amd64"  = "sha256:9c2d4e7f1b3a8056c2d4e7f1..."
```

Before starting a plugin, the manager computes the SHA-256 of the binary and
compares it to the manifest value. A mismatch aborts the plugin start with an
error.

### Limitation

The manifest and binary live in the same user-owned directory. An attacker
who can replace the binary can also update the hash in `plugin.toml`, so this
check **does not defend against a deliberate supply-chain attack**. What it
does catch:

- Accidental binary corruption (bad download, disk error)
- A partial replacement where only the binary file was swapped but the manifest
  was not (e.g. a script that copies a new binary but does not touch the
  manifest)

Full protection against deliberate tampering would require the hash to be
stored outside the plugin directory — for example, in a signed manifest whose
signature is verified against a pinned public key from the plugin author. That
is not currently implemented.

### Generating hashes

```sh
shasum -a 256 jumpy-darwin-arm64
# a3f8c1e2d4b076594f9b2e1a...  jumpy-darwin-arm64
```

Prefix the hex output with `sha256:` when writing it into `plugin.toml`.

---

## Plugin directory

Plugins are loaded from `~/.config/indigo/plugins/` (or
`$XDG_CONFIG_HOME/indigo/plugins/`). This is a user-owned path under the home
directory, so other users cannot install plugins into it without already having
write access to your home directory.

---

## Diagnostic logs

Every indigo process — the app, the client, the server, the plugin manager, and
each plugin's stderr — appends to one shared log file, rotated daily and pruned
after 24 hours without a write. It is always on; there is no flag to enable it.

```
<tmp>/indigo-plugins-<YYYY-MM-DD>.log     # or $INDIGO_LOG_DIR/…
```

Unlike the socket, this file is **not** inside a `0700` directory, so two things
are enforced on the file itself:

- **Mode `0600`, checked and not just requested.** Log contents include buffer
  text, file paths and plugin output. On a shared `/tmp` a default `0644` would
  publish those to every account on the machine. The mode given to `open(2)`
  only applies when that call *creates* the file, so an existing one — left by
  an older indigo, or created first by another user, since the dated filename
  is entirely predictable — is tightened with `fchmod` before anything is
  written. If that fails, which is what happens when the file belongs to
  someone else, the open is failed rather than the line appended.
- **`O_NOFOLLOW` on every open, for reading and writing.** The filename is
  derived from the date and so is entirely predictable. Without this, another
  user on a multi-user Linux box could pre-create it as a symlink to a file
  *you* can write, and every log line would be appended to their chosen target.
  An open that hits a squatted symlink fails with `ELOOP`, the line is dropped,
  and nothing outside the log is written. macOS is not exposed here (its
  `/var/folders/…/T` is already per-user) — which is exactly why it is enforced
  in code rather than assumed from the platform.

`report_bundle` (see [Agent Integration](agent-integration.md)) packages these
logs for a bug report. Its sync-state half cannot carry buffer contents — only a
SHA-256 and a byte count, enforced at the schema level — but the log half is
unfiltered and subject to everything above, so a bundle is worth reading before
it is shared. It says so at the top.

## Coverage summary

| Threat                                       | Status                                                                   |
|----------------------------------------------|--------------------------------------------------------------------------|
| Different user connecting to server socket   | Blocked — `0700` directory                                               |
| Different user reading the diagnostic log    | Blocked — mode `0600`, re-applied on an existing file before any write   |
| Different user pre-creating the log file     | Blocked — `fchmod` fails on their file, and the open is failed with it   |
| Symlink squatting on the log file in `/tmp`  | Blocked — `O_NOFOLLOW` on every open; the write fails rather than following |
| Different user impersonating a plugin        | Blocked — `0700` directory                                               |
| Accidental plugin binary corruption          | Detected — SHA-256 hash check (when hash is in manifest)                 |
| Deliberate plugin binary replacement         | Not blocked — attacker can update the manifest hash too                  |
| Same-user rogue process connecting to socket | Not blocked — OS allows same-UID access to same-UID sockets              |
| Network-based attacks                        | Not applicable — sockets are local-only Unix sockets                     |
| Plugin escaping sandbox                      | Not applicable — plugins run as the same user with no additional sandbox |
