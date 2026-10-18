package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/indiejames/indigo/internal/plugin"
	"github.com/indiejames/indigo/internal/plugin/plugintest"
)

// gitIn runs git in dir with a fixed identity, so the tests need no global
// config (the plugin's HOME is a temporary directory anyway).
func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=Ada", "-c", "user.email=ada@example.com"}, args...)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// newRepo makes a repository on branch, holding a.txt committed as committed,
// and returns its directory (symlinks resolved, as the editor resolves paths).
func newRepo(t *testing.T, branch, committed string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "init", "-q", "-b", branch)
	writeFile(t, filepath.Join(dir, "a.txt"), committed)
	gitIn(t, dir, "add", "a.txt")
	gitIn(t, dir, "commit", "-q", "-m", "first")
	return dir
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// view is what one decoration poll shows for lines 0..9: gutter text by line,
// and the status-bar text.
type view struct {
	gutter map[uint32]string
	status string
	blame  map[uint32]string
}

func look(m *plugin.Manager, bufID uint32) view {
	v := view{gutter: map[uint32]string{}, blame: map[uint32]string{}}
	for _, d := range m.GetDecorations(context.Background(), 7, bufID, 0, 9) {
		switch d.Kind {
		case plugin.DecorationKindGutter:
			if d.Text != "" {
				v.gutter[d.Line] = d.Text
			}
		case plugin.DecorationKindStatusBar:
			v.status = strings.TrimSpace(d.Text)
		case plugin.DecorationKindOverlay:
			v.blame[d.Line] = d.Text
		}
	}
	return v
}

func findMenuItem(items []plugin.MenuItem, command string) (plugin.MenuItem, bool) {
	for _, it := range items {
		if it.Command == command {
			return it, true
		}
		if found, ok := findMenuItem(it.Children, command); ok {
			return found, true
		}
	}
	return plugin.MenuItem{}, false
}

func invoke(t *testing.T, m *plugin.Manager, command string, bufID uint32, line uint32) {
	t.Helper()
	it, ok := findMenuItem(m.AllMenuItems(), command)
	if !ok {
		t.Fatalf("no menu item runs %s in %+v", command, m.AllMenuItems())
	}
	if handled, _, _, _, _, _, err := m.InvokeMenuAction(context.Background(), it.PluginName, it.Command, bufID, 7, line, 0); err != nil || !handled {
		t.Fatalf("%s: handled %v, err %v", command, handled, err)
	}
}

// TestGitMarksChangesAndTheBranch opens a file whose line 2 changed on disk:
// the gutter marks it, the status bar names the branch, and alt+n jumps to it.
func TestGitMarksChangesAndTheBranch(t *testing.T) {
	repo := newRepo(t, "trunk", "one\ntwo\nthree\n")
	path := filepath.Join(repo, "a.txt")
	writeFile(t, path, "one\nTWO\nthree\n")

	bridge := plugintest.NewBridge()
	m, _ := plugintest.Start(t, ".", bridge)
	bridge.OpenBuffer(1, path, "one\nTWO\nthree\n")
	m.DispatchBufferOpen(context.Background(), 1, path)

	plugintest.Eventually(t, "opening a changed file did not mark line 2 or show the branch", func() bool {
		v := look(m, 1)
		return v.gutter[1] == "+" && v.gutter[0] == "" && strings.Contains(v.status, "trunk")
	})

	_, _, line, _, hasCursor, _, err := m.HandleKey(context.Background(), "alt+n", "normal", 1, 7, 0, 0)
	if err != nil || !hasCursor || line != 1 {
		t.Errorf("alt+n from line 0: cursor %v line %d, err %v; want a jump to line 1", hasCursor, line, err)
	}
}

// TestGitDiffsUnsavedEdits covers the debounced OnChange path, which diffs the
// buffer rather than the file: the gutter follows an edit nobody has saved.
func TestGitDiffsUnsavedEdits(t *testing.T) {
	repo := newRepo(t, "main", "one\ntwo\nthree\n")
	path := filepath.Join(repo, "a.txt")

	bridge := plugintest.NewBridge()
	m, _ := plugintest.Start(t, ".", bridge)
	bridge.OpenBuffer(1, path, "one\ntwo\nthree\n")
	m.DispatchBufferOpen(context.Background(), 1, path)
	plugintest.Eventually(t, "the branch never appeared", func() bool { return look(m, 1).status != "" })
	if g := look(m, 1).gutter; len(g) != 0 {
		t.Fatalf("an unchanged file has markers: %v", g)
	}

	bridge.OpenBuffer(1, path, "one\ntwo\nthree\nfour\n") // the file on disk is untouched
	m.DispatchBufferChange(context.Background(), 1, path)
	plugintest.Eventually(t, "an unsaved added line was never marked", func() bool {
		return look(m, 1).gutter[3] == "+"
	})
}

// TestGitBlame toggles inline blame from the Command menu, then opens blame
// details for a line and picks the commit, which opens its diff.
func TestGitBlame(t *testing.T) {
	repo := newRepo(t, "main", "one\ntwo\n")
	path := filepath.Join(repo, "a.txt")

	bridge := plugintest.NewBridge()
	m, _ := plugintest.Start(t, ".", bridge)
	bridge.OpenBuffer(1, path, "one\ntwo\n")
	m.DispatchBufferOpen(context.Background(), 1, path)
	// The open is fire-and-forget: wait until the plugin has the buffer, or the
	// toggle finds nothing to toggle.
	plugintest.Eventually(t, "the branch never appeared", func() bool { return look(m, 1).status != "" })

	invoke(t, m, "git.blame", 1, 0)
	bridge.WaitForMessage(t, "Blame on")
	plugintest.Eventually(t, "blame overlays never appeared", func() bool {
		b := look(m, 1).blame
		return strings.Contains(b[0], "Ada") && strings.Contains(b[1], "Ada")
	})
	invoke(t, m, "git.blame", 1, 0)
	bridge.WaitForMessage(t, "Blame off")
	if b := look(m, 1).blame; len(b) != 0 {
		t.Errorf("blame still drawn after toggling off: %v", b)
	}

	invoke(t, m, "git.blame_details", 1, 1)
	popup := bridge.WaitForPopup(t, 1)
	if popup.Title != "Blame: line 2" || len(popup.Items) != 1 || !strings.Contains(popup.Items[0].Label, "Ada") {
		t.Fatalf("blame details popup = %+v, want line 2's commit by Ada", popup)
	}
	popup.Select(popup.Items[0].Data)
	plugintest.Eventually(t, "picking the commit did not open its diff", func() bool { return len(bridge.Opened()) == 1 })
	diff, err := os.ReadFile(bridge.Opened()[0].Path)
	if err != nil || !strings.Contains(string(diff), "first") || !strings.Contains(string(diff), "+two") {
		t.Errorf("opened %s (%v), want git show of the first commit:\n%s", bridge.Opened()[0].Path, err, diff)
	}
	os.Remove(bridge.Opened()[0].Path) //nolint:errcheck
}

// TestGitTracksEachBufferInItsOwnRepository is the regression test for the
// plugin keeping one repository root, the first one any buffer opened in, and
// using it for every later change and save. A workspace holding two
// repositories lost the second one's markers on its first edit, because
// `git ls-files` ran in the wrong repository and reported the file untracked.
func TestGitTracksEachBufferInItsOwnRepository(t *testing.T) {
	first := newRepo(t, "main", "a\nb\n")
	second := newRepo(t, "other", "x\ny\n")
	firstPath, secondPath := filepath.Join(first, "a.txt"), filepath.Join(second, "a.txt")

	bridge := plugintest.NewBridge()
	m, _ := plugintest.Start(t, ".", bridge)
	ctx := context.Background()
	bridge.OpenBuffer(1, firstPath, "a\nb\n")
	m.DispatchBufferOpen(ctx, 1, firstPath)
	plugintest.Eventually(t, "the first repository's branch never appeared", func() bool { return look(m, 1).status != "" })

	writeFile(t, secondPath, "x\nY\n")
	bridge.OpenBuffer(2, secondPath, "x\nY\n")
	m.DispatchBufferOpen(ctx, 2, secondPath)
	plugintest.Eventually(t, "opening a changed file in the second repository did not mark it", func() bool {
		return look(m, 2).gutter[1] == "+"
	})

	// An unsaved edit in the second repository adds a line.
	bridge.OpenBuffer(2, secondPath, "x\nY\nz\n")
	m.DispatchBufferChange(ctx, 2, secondPath)
	plugintest.Eventually(t, "an edit in the second repository lost its markers", func() bool {
		g := look(m, 2).gutter
		return g[1] == "+" && g[2] == "+"
	})

	// And saving it diffs against its own repository too.
	writeFile(t, secondPath, "x\nY\nz\n")
	m.DispatchBufferSave(ctx, 2, secondPath)
	plugintest.Eventually(t, "a save in the second repository lost its markers", func() bool {
		g := look(m, 2).gutter
		return g[1] == "+" && g[2] == "+"
	})
}

// TestGitNamesTheBranchOfALinkedWorktree is the regression test for reading
// <root>/.git/HEAD as a file path: in a linked worktree .git is a file pointing
// at the real git directory, so no branch was ever shown there.
func TestGitNamesTheBranchOfALinkedWorktree(t *testing.T) {
	repo := newRepo(t, "main", "one\n")
	wt := filepath.Join(t.TempDir(), "wt")
	gitIn(t, repo, "worktree", "add", "-q", "-b", "feature", wt)
	wt, err := filepath.EvalSymlinks(wt)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(wt, "a.txt")

	bridge := plugintest.NewBridge()
	m, _ := plugintest.Start(t, ".", bridge)
	bridge.OpenBuffer(1, path, "one\n")
	m.DispatchBufferOpen(context.Background(), 1, path)
	plugintest.Eventually(t, "a file in a linked worktree showed no branch", func() bool {
		return strings.Contains(look(m, 1).status, "feature")
	})
}

// TestGitShipsEveryMenuCommand pins the manifest to the code: each command the
// plugin.toml menu names must be one Init registers, which invoking it proves.
func TestGitShipsEveryMenuCommand(t *testing.T) {
	bridge := plugintest.NewBridge()
	m, _ := plugintest.Start(t, ".", bridge)
	var commands []string
	var walk func([]plugin.MenuItem)
	walk = func(items []plugin.MenuItem) {
		for _, it := range items {
			if it.Command != "" {
				commands = append(commands, it.Command)
			}
			walk(it.Children)
		}
	}
	walk(m.AllMenuItems())
	slices.Sort(commands)
	if want := []string{"git.blame", "git.blame_details", "git.diff"}; !slices.Equal(commands, want) {
		t.Fatalf("menu commands = %q, want %q", commands, want)
	}
	for _, c := range commands {
		// Buffer 9 is unknown to the plugin; every handler answers it anyway.
		invoke(t, m, c, 9, 0)
	}
}

// TestGitNoticesABranchSwitchInEveryRepository covers pollLoop, which watched
// only the first repository's HEAD: switching branches in a second one never
// reached its buffers' status bar.
func TestGitNoticesABranchSwitchInEveryRepository(t *testing.T) {
	first := newRepo(t, "main", "a\n")
	second := newRepo(t, "other", "x\n")
	firstPath, secondPath := filepath.Join(first, "a.txt"), filepath.Join(second, "a.txt")

	bridge := plugintest.NewBridge()
	m, _ := plugintest.Start(t, ".", bridge)
	ctx := context.Background()
	bridge.OpenBuffer(1, firstPath, "a\n")
	bridge.OpenBuffer(2, secondPath, "x\n")
	m.DispatchBufferOpen(ctx, 1, firstPath)
	m.DispatchBufferOpen(ctx, 2, secondPath)
	plugintest.Eventually(t, "each buffer did not show its own repository's branch", func() bool {
		return strings.Contains(look(m, 1).status, "main") && strings.Contains(look(m, 2).status, "other")
	})

	gitIn(t, second, "checkout", "-q", "-b", "switched")
	plugintest.Eventually(t, "a branch switch in the second repository never showed", func() bool {
		return strings.Contains(look(m, 2).status, "switched")
	})
	if s := look(m, 1).status; !strings.Contains(s, "main") {
		t.Errorf("the first repository's buffer now shows %q, want main still", s)
	}
}
