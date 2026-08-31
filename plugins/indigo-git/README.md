# indigo-git

Git integration plugin for the Indigo editor. Shows the current branch in the status bar, decorates modified lines in the gutter, and adds inline blame, a diff view, and hunk navigation.

## Status bar

The right side of the status bar shows the current git branch when the open file is inside a git repository:

```
 branch-name
```

The branch name is preceded by a Nerd Fonts branch glyph (``). If Nerd Fonts are not installed the glyph may not render correctly; it can be ignored.

## Gutter decorations

Each line is decorated based on its git diff status relative to HEAD:

| Symbol | Color  | Meaning                                      |
|--------|--------|----------------------------------------------|
| `│`    | Green  | Line was added (not present in HEAD)         |
| `│`    | Blue   | Line was modified (content changed from HEAD)|
| `▾`    | Red    | Marker for a deleted block (lines removed above this position) |
| ` `    | —      | Line is unchanged                            |

The gutter column appears only when at least one changed line is detected in the current file.

## Hunk navigation

- `alt+n` — jump the cursor to the next changed hunk
- `alt+p` — jump the cursor to the previous changed hunk

Both wrap around the buffer (from the last hunk back to the first, and vice versa) and use the same diff data that drives the gutter markers above.

## Inline blame

`alt+b`, or **Command menu → Git → Toggle blame**, toggles per-buffer end-of-line blame annotations:

```
func doThing() {              a1b2c3d Jane Doe, 3 days ago
```

Uncommitted lines show `uncommitted` instead, styled in amber. Blame reflects the file as last saved to disk — it does not reflect unsaved edits, since recomputing a full-file `git blame` on every keystroke would be too expensive. It's automatically refreshed on save and when the branch or index changes for any buffer currently showing it.

## Blame details

**Command menu → Git → Blame details** shows a popup with the full commit for the line under the cursor: short hash, author, date, and commit summary. Selecting the popup entry opens that commit's full diff (`git show`) in a new tab. This works independently of the inline toggle above — it blames just the current line, not the whole file.

## Diff view

**Command menu → Git → Diff** opens `git diff HEAD` for the current file in a new tab, so you can review changes without leaving the editor. This (and the blame-details commit view) opens a scratch temp file, not a file tracked by git — saving it has no effect on the repository.

## Requirements

- `git` must be in `PATH`.
- The file being edited must be inside a git repository.
- The plugin binary must be built and placed where `indigo` can find it (see the main indigo documentation for plugin installation).
