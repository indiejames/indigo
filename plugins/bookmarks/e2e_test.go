package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/indiejames/indigo/internal/plugin"
	"github.com/indiejames/indigo/internal/plugin/plugintest"
)

// TestBookmarksEndToEnd drives the built plugin through the editor's own paths:
// alt+m and its name prompt add a bookmark, the gutter shows it, the Command
// menu lists it and jumps to it, an edit above moves it, alt+m again removes
// it, and every step lands in bookmarks.json under the plugin's (temporary)
// home.
func TestBookmarksEndToEnd(t *testing.T) {
	bridge := plugintest.NewBridge()
	m, home := plugintest.Start(t, ".", bridge)
	ctx := context.Background()
	const bufID, clientID = 1, 7
	path := filepath.Join(t.TempDir(), "notes.txt")
	bridge.OpenBuffer(bufID, path, "zero\none\ntwo\nthree\n")
	saved := filepath.Join(home, ".config", "indigo", "plugins", "bookmarks", "bookmarks.json")

	gutter := func() []uint32 {
		var lines []uint32
		for _, d := range m.GetDecorations(ctx, clientID, bufID, 0, 10) {
			if d.Kind == plugin.DecorationKindLeftGutter && d.Text == "▶" {
				lines = append(lines, d.Line)
			}
		}
		return lines
	}
	onDisk := func() []savedBookmark {
		data, err := os.ReadFile(saved)
		if err != nil {
			return nil
		}
		var s []savedBookmark
		json.Unmarshal(data, &s) //nolint:errcheck
		return s
	}

	// The menu with nothing to list says so instead of showing an empty popup.
	item := menuItem(t, m, "Bookmarks")
	if _, _, _, _, _, _, err := m.InvokeMenuAction(ctx, item.PluginName, item.Command, bufID, clientID, 0, 0); err != nil {
		t.Fatal(err)
	}
	bridge.WaitForMessage(t, "No bookmarks")

	// alt+m on line 2 prompts for a name; confirming adds the bookmark.
	if handled, _, _, _, _, _, err := m.HandleKey(ctx, "alt+m", "normal", bufID, clientID, 2, 1); err != nil || !handled {
		t.Fatalf("alt+m: handled %v, err %v", handled, err)
	}
	bridge.WaitForPrompt(t, 1).Confirm("todo")
	plugintest.Eventually(t, "the bookmark never reached the gutter or the file", func() bool {
		g, d := gutter(), onDisk()
		return len(g) == 1 && g[0] == 2 && len(d) == 1 && d[0] == savedBookmark{FilePath: path, Line: 2, Col: 1, Note: "todo"}
	})

	// The menu lists it, and picking it opens the file at its line.
	if _, _, _, _, _, _, err := m.InvokeMenuAction(ctx, item.PluginName, item.Command, bufID, clientID, 0, 0); err != nil {
		t.Fatal(err)
	}
	popup := bridge.WaitForPopup(t, 1)
	if len(popup.Items) != 1 || popup.Items[0].Label != "notes.txt:3" || popup.Items[0].Sublabel != "todo" {
		t.Fatalf("popup items = %+v, want one notes.txt:3 named todo", popup.Items)
	}
	popup.Select(popup.Items[0].Data)
	plugintest.Eventually(t, "picking the bookmark did not open its file", func() bool {
		o := bridge.Opened()
		return len(o) == 1 && o[0] == plugintest.Opened{Path: path, Line: 2}
	})

	// Two lines inserted above it move it down two.
	m.DispatchEditEvent(ctx, bufID, path, plugin.EditRange{FromLine: 0, FromCol: 4, ToLine: 0, ToCol: 4, LineDelta: 2})
	plugintest.Eventually(t, "an insert above did not move the bookmark", func() bool {
		g, d := gutter(), onDisk()
		return len(g) == 1 && g[0] == 4 && len(d) == 1 && d[0].Line == 4
	})

	// alt+m on its new line removes it.
	if _, _, _, _, _, _, err := m.HandleKey(ctx, "alt+m", "normal", bufID, clientID, 4, 0); err != nil {
		t.Fatal(err)
	}
	plugintest.Eventually(t, "alt+m on a bookmarked line did not remove it", func() bool {
		return len(gutter()) == 0 && len(onDisk()) == 0
	})
	if n := len(bridge.Messages()); n != 1 {
		t.Errorf("messages = %+v, want only the earlier No bookmarks: removing asks nothing", bridge.Messages())
	}
}

// TestBookmarksDeletedLineDropsItsBookmark covers onEdit's deletion path
// through the plugin process: a bookmark inside deleted lines disappears from
// the gutter and the file, and one below them moves up.
func TestBookmarksDeletedLineDropsItsBookmark(t *testing.T) {
	bridge := plugintest.NewBridge()
	m, home := plugintest.Start(t, ".", bridge)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "a.txt")
	bridge.OpenBuffer(1, path, "0\n1\n2\n3\n4\n5\n")

	for i, line := range []uint32{1, 4} {
		m.HandleKey(ctx, "alt+m", "normal", 1, 7, line, 0) //nolint:errcheck
		bridge.WaitForPrompt(t, i+1).Confirm("")
	}
	// Lines 1 and 2 deleted (from 1:0 to 3:0): the first bookmark goes, the
	// second moves 4 → 2.
	m.DispatchEditEvent(ctx, 1, path, plugin.EditRange{FromLine: 1, ToLine: 3, LineDelta: -2})

	saved := filepath.Join(home, ".config", "indigo", "plugins", "bookmarks", "bookmarks.json")
	plugintest.Eventually(t, "the deletion did not drop one bookmark and move the other", func() bool {
		var lines []uint32
		for _, d := range m.GetDecorations(ctx, 7, 1, 0, 10) {
			if d.Kind == plugin.DecorationKindLeftGutter {
				lines = append(lines, d.Line)
			}
		}
		data, _ := os.ReadFile(saved)
		var s []savedBookmark
		json.Unmarshal(data, &s) //nolint:errcheck
		return len(lines) == 1 && lines[0] == 2 && len(s) == 1 && s[0].Line == 2
	})
}

func menuItem(t *testing.T, m *plugin.Manager, label string) plugin.MenuItem {
	t.Helper()
	for _, it := range m.AllMenuItems() {
		if it.Label == label {
			return it
		}
	}
	t.Fatalf("no %q menu item in %+v", label, m.AllMenuItems())
	return plugin.MenuItem{}
}

// TestBookmarksFollowJoinsAndOpenedLines is the regression test for the
// line-only edit event: joining a bookmarked line to the next one suspended its
// bookmark, as if the line had been deleted, and a line opened above a
// bookmarked one left the bookmark on the new, empty line. With the edit's
// range on the event, both follow the text.
func TestBookmarksFollowJoinsAndOpenedLines(t *testing.T) {
	bridge := plugintest.NewBridge()
	m, _ := plugintest.Start(t, ".", bridge)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "a.txt")
	bridge.OpenBuffer(1, path, "zero\none\ntwo\n")
	m.HandleKey(ctx, "alt+m", "normal", 1, 7, 1, 0) //nolint:errcheck
	bridge.WaitForPrompt(t, 1).Confirm("")
	lines := func() []uint32 {
		var out []uint32
		for _, d := range m.GetDecorations(ctx, 7, 1, 0, 10) {
			if d.Kind == plugin.DecorationKindLeftGutter {
				out = append(out, d.Line)
			}
		}
		return out
	}
	plugintest.Eventually(t, "the bookmark never appeared on line 1", func() bool {
		l := lines()
		return len(l) == 1 && l[0] == 1
	})

	// J on line 1: the newline at its end (1:3 to 2:0) is deleted.
	m.DispatchEditEvent(ctx, 1, path, plugin.EditRange{FromLine: 1, FromCol: 3, ToLine: 2, ToCol: 0, LineDelta: -1})
	// O on line 1: a newline inserted at its start.
	m.DispatchEditEvent(ctx, 1, path, plugin.EditRange{FromLine: 1, FromCol: 0, ToLine: 1, ToCol: 0, LineDelta: 1})
	plugintest.Eventually(t, "the bookmark did not survive the join and move below the opened line", func() bool {
		l := lines()
		return len(l) == 1 && l[0] == 2
	})
}
