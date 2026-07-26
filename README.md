# indigo

A terminal text editor written in go with modal editing and built-in language server support. Inspired by [Vim](https://www.vim.org/), [Kakoune](https://kakoune.org/), and [Helix](https://helix-editor.com/).

## Who it's for

indigo is for developers who:

- Prefer staying in the terminal and are comfortable with modal editing
- Want **LSP features out of the box** — diagnostics, hover docs, go-to-definition, completions — without writing any config
- Work across multiple languages and want syntax highlighting and formatting to just work

If you want an editor that does the right thing for your language automatically (gopls for Go, rust-analyzer for Rust, typescript-language-server for TypeScript, etc.) the moment you open a file, indigo is for you.

## Why another editor?

Mostly I just wanted to try some design ideas I had for an editor. Specifically a terminal based client-server
architecture with core features that everyone seems to agree on (like syntax highlighting) and
a fast plugin system for the features that not everyone needs (but some people want). None of these ideas are new. Helix provides great core features,
but doesn't use a client-server model and doesn't have plugins (yet). Kakoune does, but I wanted a different extension system and more out of the box.
[Visual Studio Code](https://code.visualstudio.com/) is great and it's easy to build extensions for it,
but it doesn't run in the terminal and has a pretty big footprint.

In my day-to-day work, I always found myself working on big project code in VS Code, and using Vim or Helix for quick edits to single files, 
especially over ssh. I wanted something that worked for _me_ for both types of editing.

Also, I wanted to build something real in Go, and to use some of the great projects I have read about, like [Cap'n Proto](https://capnproto.org/) and [Bubble Tea](https://github.com/charmbracelet/bubbletea).

## Install

Currently `indigo` has only been tested on macOS. It is highly likely to work on Linux, less likely to work on Windows.

> **Note:** Binary releases are not yet available. For now, build from source:

```
make install
```

The binary is named `indigo`. Requires Go 1.21+.

## Quick start

I highly recommend that you alias `indigo` to `io`
```
alias io indigo
```

After that

```
io file.go          # open a file
io .                # open directory (shows file picker)
io +42 file.go      # open a file at line 42
```

indigo uses a **client/server model** similar to Kakoune: the first invocation starts a background server for your workspace (rooted at the nearest `.git` directory or the directory of the edited file if not part of a git repository); subsequent `indigo` sessions in the same workspace connect to the existing server. The server exits automatically when all editor sessions close. If two client windows have the same file open,
changes in one will show up in the other.

The upshot of this is that instead of `indigo` managing editor panes, you can use your current terminal window/pane system to manage layout. I use [zellij](https://zellij.dev/).

### Editing model

indigo follows the **select → operate** model from Kakoune and Helix, rather than the operator → motion model of Vim. You always select text first, then act on it:

```
w        select the word under the cursor
d        delete it

x        select the current line
c        delete it and enter insert mode

w w      advance the selection to the next word
W        extend the selection head forward to the next word end
y        copy it to the clipboard

x x x    select three lines (repeat extends to the next line)
X        extend the selection backward to include the previous line
```

If you're coming from Vim, the main adjustment is that `d` and `c` act on whatever is currently selected, not on a following motion. If nothing is selected, `d` deletes the character under the cursor, and `c` modifies the character under the cursor.

## Architecture

indigo separates the editor into a **server** and one or more **clients** communicating over a Unix domain socket. The server owns all open buffers, runs language servers (LSP), and handles crash recovery. Each terminal window is a lightweight client that renders the UI and sends edits.

The client–server protocol is built on **[Cap'n Proto](https://capnproto.org)**, chosen specifically for its performance characteristics:

- **Zero-copy serialization** — Cap'n Proto's wire format *is* the in-memory representation. There is no parsing or marshaling step; the server can hand a response directly to the network layer without touching each byte.
- **Negligible latency** — Because there is no encode/decode overhead, a round-trip for a keystroke or an LSP hover request adds no measurable latency on top of the Unix socket itself.
- **Schema-defined interface** — The RPC interface is described in a `.capnp` schema file, giving both sides a typed, versioned contract with built-in support for backwards-compatible evolution.

The practical result is that multiple `indigo` windows on the same workspace share one server process with no perceptible communication cost — edits, diagnostics, and completions feel as immediate as if everything were running in a single process.

## Key bindings

### Normal mode

**Cursor movement** — always clears the selection.

| Key                 | Action                                          |
|---------------------|-------------------------------------------------|
| `h` `j` `k` `l`     | Move left / down / up / right                   |
| `b`                 | Move to previous word start (crosses lines)     |
| `e`                 | Move to end of current/next word (crosses lines)|
| `0` `$`             | Start / end of line                             |
| `gg`                | Top of file                                     |
| `G`                 | End of file                                     |
| `Ctrl+f` / `Ctrl+b` | Page down / up                                  |
| `gh`                | Go to line start                                |
| `gl`                | Go to line end                                  |
| `gs`                | Go to first non-whitespace character on line    |
| `gd`                | Go to definition (LSP)                          |

**Selection** — create or extend a selection; the cursor is always at the head.

| Key      | Action                                                           |
|----------|------------------------------------------------------------------|
| `w`      | Select word at cursor; repeat to advance to the next word        |
| `W`      | Extend selection head forward to end of next word                |
| `B`      | Extend selection head backward to start of previous word         |
| `x`      | Select current line; repeat to extend selection to the next line |
| `X`      | Extend line selection backward to include the previous line      |
| `%`      | Select the entire file                                           |
| `;`      | Collapse selection to cursor (keeps cursor, clears selection)    |
| `Alt+;`  | Flip selection: swap anchor and head                             |

**Operators** — act on the current selection; clears search highlights.

| Key | Action                                       |
|-----|----------------------------------------------|
| `d` | Delete selection (or character under cursor) |
| `c` | Delete selection and enter insert mode       |
| `y` | Copy selection to system clipboard           |

**Other**

| Key          | Action                                       |
|--------------|----------------------------------------------|
| `i`          | Enter insert mode                            |
| `a`          | Enter insert mode, cursor after current char |
| `A`          | Enter insert mode at end of line             |
| `o` / `O`    | Open new line below / above                  |
| `u` / `U`    | Undo / redo                                  |
| `K`          | Show hover documentation (LSP)               |
| `/`          | Enter search mode                            |
| `n` / `N`    | Next / previous search match                 |
| `s`          | Open the global search & replace dialog      |
| `Esc`        | Clear selection and search highlights        |
| `Ctrl+s`     | Save                                         |
| `Ctrl+p`     | Open file picker                             |
| `]b` / `[b`  | Next / previous buffer                       |
| `:`          | Enter command mode                           |

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

The match count `[N/total]` is shown at the right of the search bar while typing, and in the status bar centre while navigating results. If the regex is invalid, `[invalid]` is shown instead.

After confirming, use `n` / `N` in normal mode to move to the next / previous match. Pressing `Esc`, entering insert mode, or performing an edit clears the highlights and stops `n` / `N` navigation.

### Workspace search

Type `:grep <pattern>` (or `:find <pattern>`) to search across every file in the workspace. The same pattern syntax applies — plain text (smart-case) or `\expr\` for Go regex.

```
:grep TODO               search all files for "TODO"
:find func.*Handler      regex search (leading \)
:grep error *.go         restrict to .go files
:grep TODO src/          restrict to the src/ directory
:grep TODO **/*.ts       restrict to .ts files anywhere in the tree
```

The optional trailing token is treated as a file glob when it contains `*`, `?`, `[`, or ends with `/`. The active glob is shown in the picker title as `in:*.go`. If you run `:grep` with no argument, it reuses the last within-buffer search pattern.

A results picker opens immediately showing a "Searching…" indicator while the search runs in the background. Once complete:

| Key       | Action                     |
|-----------|----------------------------|
| `j` / `↓` | Move to next result        |
| `k` / `↑` | Move to previous result    |
| `Enter`   | Open file at matching line |
| `Esc`     | Close picker               |

Selecting a result opens the file with the cursor at the match column and the matched text selected.

Each result is shown as `file:line: content`. Ignored directories (`.git`, `vendor`, `node_modules`, etc.) and binary files are skipped automatically. Results are capped at 500 matches.

When [ripgrep](https://github.com/BurntSushi/ripgrep) (`rg`) is on your PATH it is used as the search backend — it is significantly faster on large repos and automatically respects `.gitignore`. indigo falls back to a built-in Go walker if `rg` is not available.

### Search & replace dialog

Press `s` in normal mode to open a floating search & replace dialog for the whole workspace.

```
 ╭────────────────────────────╮  [ ] Aa
 │ Search                     │  [ ] .*
 ╰────────────────────────────╯
 ▸ Replace
```

- Type a pattern and press **Enter** to search across the workspace (same backend as `:grep` — ripgrep when available).
- The **Aa** checkbox toggles case sensitivity; **.*** toggles regex mode. Unlike `/` and `:grep`, these are explicit checkboxes rather than inferred from the pattern text.
- The **▸ Replace** disclosure expands a second field for the replacement text, plus an **All** button.
- **Tab** / **Shift+Tab** cycle focus between the search field, the two checkboxes, the replace disclosure, the replace field, the All button, and the results list. **Space** toggles whichever checkbox/disclosure is focused.
- Results appear below as a scrollable list, one line per match. Once a replacement is typed, each line shows the matched text struck through followed by the replacement.
- With focus on a result, **Enter** applies that one replacement and jumps to it — opening the file as a normal buffer if it wasn't already open, so the change is undoable with `u` like any other edit.
- **All** applies the replacement to every result at once, after a confirmation showing how many files/lines will change. Files already open elsewhere are edited in memory (left dirty, not saved); files with no open buffer are written to disk directly. Any match whose text no longer matches what was found (e.g. edited concurrently) is skipped and reported rather than applied blindly.
- **Esc** closes the dialog.

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
| `:grep [pattern] [glob]`   | Workspace search           |
| `:find [pattern] [glob]`   | Workspace search (alias)   |
| `:<n>`                     | Jump to line number        |

### Mouse

| Action       | Result      |
|--------------|-------------|
| Click        | Move cursor |
| Click + drag | Select text |
| Double-click | Select word |

## Features

**Search** — Press `/` to search within the current buffer. Supports incremental literal search with smart-case and regex search (prefix with `\`). Match count displayed in the status bar; `n` / `N` navigates between matches. Use `:grep [pattern]` for workspace-wide search across all files.

**Search & replace** — Press `s` for a floating dialog that searches and replaces across the whole workspace, with independent case/regex checkboxes, a live per-match diff preview, and either one-at-a-time or all-at-once apply.

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
