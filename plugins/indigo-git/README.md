# indigo-git

Git integration plugin for the Indigo editor. Shows the current branch in the status bar and decorates modified lines in the gutter.

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

## Requirements

- `git` must be in `PATH`.
- The file being edited must be inside a git repository.
- The plugin binary must be built and placed where `indigo` can find it (see the main indigo documentation for plugin installation).
