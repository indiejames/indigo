# indigo

A terminal text editor with modal editing and built-in language server support. Inspired by [Vim](https://www.vim.org/), [Kakoune](https://kakoune.org/), and [Helix](https://helix-editor.com/).

**Note:** indigo is early-stage software — expect rough edges.

**Caveat:** While indigo currently does not provide support for using AI tools like [GitHub CoPilot](https://github.com/features/copilot) or [Claude.AI](https://claude.ai/new), Claude.AI _was_ used in the development of indigo. If you are opposed to the use of AI tools in software development then you might want to look elsewhere.

## Who it's for

indigo is for developers who:

- Prefer staying in the terminal and are comfortable with modal editing
- Want **LSP features out of the box** — diagnostics, hover docs, go-to-definition, completions — without writing any config
- Work across multiple languages and want syntax highlighting and formatting to just work

If you want an editor that does the right thing for your language automatically (gopls for Go, rust-analyzer for Rust, typescript-language-server for TypeScript, etc.) the moment you open a file, indigo is for you.

## Why another editor?

Mostly I just wanted to try some design ideas I had for an editor. Specifically a terminal based client-server
architecture with core features that everyone seems to agree on (like syntax highlighting) and
a fast plugin system for extensibility. None of these ideas are new. Helix provides great core features,
but doesn't use a client-server model. Kakoune does, but I wanted a different extension system.
[Visual Studio Code](https://code.visualstudio.com/) is great and it's easy to build extensions for it,
but it doesn't run in the terminal.

Also, I wanted to build something real in Go, and to use some of the great projects I have read about, like [Cap'n Proto](https://capnproto.org/) and [Bubble Tea](https://github.com/charmbracelet/bubbletea).

## Install

> **Note:** Binary releases are not yet available. For now, build from source:

```
make install
```

The binary is named `indigo`. Requires Go 1.21+.

## Quick start

```
indigo file.go          # open a file
indigo .                # open directory (shows file picker)
indigo +42 file.go      # open a file at line 42
```

indigo uses a **client/server model** similar to Kakoune: the first invocation starts a background server for your workspace (rooted at the nearest `.git` directory or the directory of the edited file if not part of a git repository); subsequent `indigo` sessions in the same workspace connect to the existing server. The server exits automatically when all editor sessions close. If two client windows have the same file open,
changes in one will show up in the other.

The upshot of this is that instead of `indigo` managing editor panes, you can use your current terminal window/pane system to manage layout.

### Editing model

indigo follows the **select → operate** model from Kakoune and Helix, rather than the operator → motion model of Vim. You always select text first, then act on it:

```
w        select the word under the cursor
d        delete it

x        select the current line
c        delete it and enter insert mode

w w      advance the selection to the next word
y        copy it to the clipboard
```

If you're coming from Vim, the main adjustment is that `d` and `c` act on whatever is currently selected, not on a following motion. If nothing is selected, `d` deletes the character under the cursor.

## Architecture

indigo separates the editor into a **server** and one or more **clients** communicating over a Unix domain socket. The server owns all open buffers, runs language servers (LSP), and handles crash recovery. Each terminal window is a lightweight client that renders the UI and sends edits.

The client–server protocol is built on **[Cap'n Proto](https://capnproto.org)**, chosen specifically for its performance characteristics:

- **Zero-copy serialization** — Cap'n Proto's wire format *is* the in-memory representation. There is no parsing or marshaling step; the server can hand a response directly to the network layer without touching each byte.
- **Negligible latency** — Because there is no encode/decode overhead, a round-trip for a keystroke or an LSP hover request adds no measurable latency on top of the Unix socket itself.
- **Schema-defined interface** — The RPC interface is described in a `.capnp` schema file, giving both sides a typed, versioned contract with built-in support for backwards-compatible evolution.

The practical result is that multiple `indigo` windows on the same workspace share one server process with no perceptible communication cost — edits, diagnostics, and completions feel as immediate as if everything were running in a single process.

## Key bindings

### Normal mode

| Key                 | Action                                       |
|---------------------|----------------------------------------------|
| `i`                 | Enter insert mode                            |
| `A`                 | Enter insert mode at end of line             |
| `o` / `O`           | Open new line below / above                  |
| `h` `j` `k` `l`     | Move left / down / up / right                |
| `0` `$`             | Start / end of line                          |
| `G`                 | End of file                                  |
| `gg`                | Top of file                                  |
| `Ctrl+f` / `Ctrl+b` | Page down / up                               |
| `w`                 | Select word (repeat to advance to next word) |
| `x`                 | Select current line                          |
| `d`                 | Delete selection (or character under cursor) |
| `c`                 | Delete selection and enter insert mode       |
| `y`                 | Copy selection to system clipboard           |
| `u` / `U`           | Undo / redo                                  |
| `K`                 | Show hover documentation (LSP)               |
| `gd`                | Go to definition (LSP)                       |
| `/`                 | Enter search mode                            |
| `n` / `N`           | Next / previous search match                 |
| `Ctrl+s`            | Save                                         |
| `Ctrl+p`            | Open file picker                             |
| `]b` / `[b`         | Next / previous buffer                       |
| `:`                 | Enter command mode                           |
| `Esc`               | Clear selection                              |

### Insert mode

| Key                 | Action                          |
|---------------------|---------------------------------|
| `Esc`               | Return to normal mode           |
| `Ctrl+s`            | Save                            |
| `Ctrl+Space`        | Trigger completion              |
| `Tab` / `Shift+Tab` | Next / previous completion item |
| `Enter`             | Accept completion               |

### Search mode

Type `/` in normal mode to open the search bar at the bottom of the screen.

| Key         | Action                                         |
|-------------|------------------------------------------------|
| _(typing)_  | Incrementally update pattern and jump to match |
| `Enter`     | Confirm and return to normal mode              |
| `Esc`       | Cancel and restore cursor to original position |
| `Backspace` | Delete one character from the pattern          |

**Smart-case:** a lowercase pattern matches case-insensitively; any uppercase letter makes the match case-sensitive.

**Regex search:** prefix the pattern with `\` to use [Go regular expressions](https://pkg.go.dev/regexp/syntax):

```
/hello          literal, case-insensitive
/Hello          literal, case-sensitive (uppercase triggers sensitivity)
/\func \w+      regex: "func " followed by word characters
/\[0-9]{3}-\d{4}  regex: phone-number fragment
/\(?i)TODO      regex with explicit case-insensitive flag
```

The match count `[N/total]` is shown at the right of the search bar. If the regex is invalid, `[invalid]` is shown instead.

After confirming, use `n` / `N` in normal mode to move to the next / previous match.

### Command mode

Type `:` in normal mode, then one of:

| Command                    | Action                     |
|----------------------------|----------------------------|
| `:w` `:write` `:s` `:save` | Save                       |
| `:q` `:quit`               | Quit (fails if unsaved)    |
| `:q!` `:quit!`             | Quit discarding changes    |
| `:wq` `:x`                 | Save and quit              |
| `:qa` `:quit-all`          | Close all buffers and quit |
| `:qa!` `:quit-all!`        | Force close all and quit   |
| `:wqa`                     | Save all and quit          |
| `:e` `:edit`               | Open file picker           |
| `:fmt` `:format`           | Format current file        |
| `:<n>`                     | Jump to line number        |

### Mouse

| Action       | Result      |
|--------------|-------------|
| Click        | Move cursor |
| Click + drag | Select text |
| Double-click | Select word |

## Features

**Search** — Press `/` to search within the current buffer. Supports incremental literal search with smart-case and regex search (prefix with `\`). Match count displayed in the status bar; `n` / `N` navigates between matches.

**Syntax highlighting** — 40+ languages via Tree-sitter grammars. See [Language Support](docs/language-support.md).

**LSP integration** — Language servers start automatically when you open a supported file (no config needed if the server is in your PATH). Provides:
- Inline diagnostics with gutter markers
- Hover documentation (`K`)
- Go-to-definition (`gd`) — opens in a new buffer, not a new session
- Signature help — appears automatically when you type `(`
- Completions — automatic or on-demand with `Ctrl+Space`

**Auto-formatting** — On `:w` with `format_on_save = true`, indigo runs the appropriate formatter (gofmt, prettier, rustfmt, etc.) automatically. See [Language Support](docs/language-support.md) for the full list.

**Multi-buffer** — Open multiple files in the same session. A tab bar appears when more than one buffer is open. Use `]b` / `[b` or `Ctrl+P` to navigate.

**Crash recovery** — File content is written to a recovery directory every 5 seconds. If indigo exits uncleanly, the next open will offer to restore your unsaved work.

**Shared server** — Opening the same workspace in multiple terminal windows shares one server process. Buffers are synchronized across sessions.

## Configuration

Config file: `~/.config/indigo/config.toml`

```toml
line_numbers    = true   # show line numbers in gutter
hide_tabs       = false  # hide tab bar even when multiple buffers are open
fuzzy_search    = true   # fuzzy matching in the file picker
format_on_save  = false  # run formatter automatically on :w
```

Full configuration reference, including how to add custom language servers and formatters: [Configuration](docs/configuration.md).
