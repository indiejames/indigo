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
/tmp/indigo-<uid>-<workspace-hash>/server.sock
```

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

## Coverage summary

| Threat                                       | Status                                                                   |
|----------------------------------------------|--------------------------------------------------------------------------|
| Different user connecting to server socket   | Blocked — `0700` directory                                               |
| Different user impersonating a plugin        | Blocked — `0700` directory                                               |
| Accidental plugin binary corruption          | Detected — SHA-256 hash check (when hash is in manifest)                 |
| Deliberate plugin binary replacement         | Not blocked — attacker can update the manifest hash too                  |
| Same-user rogue process connecting to socket | Not blocked — OS allows same-UID access to same-UID sockets              |
| Network-based attacks                        | Not applicable — sockets are local-only Unix sockets                     |
| Plugin escaping sandbox                      | Not applicable — plugins run as the same user with no additional sandbox |
