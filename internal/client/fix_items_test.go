package client

import (
	"testing"

	"github.com/indiejames/indigo/internal/document"
)

// TestFixItemsIgnoredOnBufferOrCursorChange is a regression test: unlike its
// neighbor completionResolvedMsg (which already guards against exactly this),
// fixItemsMsg had no bufID/cursor check at all — a slow Code Actions (space+a)
// response could pop up showing fixes for wherever the request was made, not
// where the cursor is now, if the user moved on before it arrived.
func TestFixItemsIgnoredOnBufferOrCursorChange(t *testing.T) {
	base := func() Model {
		m := newTestModel("hello\n")
		m.bufID = 1
		m.cursor = document.Pos{Line: 0, Col: 2}
		return m
	}
	items := []ClientFixItem{{Label: "Replace if/else with max"}}
	requestedAt := document.Pos{Line: 0, Col: 2}
	requestedVersion := base().buf.Version()

	// Different buffer → ignored.
	res, _ := base().Update(fixItemsMsg{items: items, bufID: 2, at: requestedAt, version: requestedVersion})
	if got := res.(Model); len(got.fixItems) != 0 {
		t.Errorf("fixItems applied despite bufID mismatch: %+v", got.fixItems)
	}

	// Cursor moved since the request → ignored.
	m := base()
	m.cursor = document.Pos{Line: 0, Col: 4}
	res2, _ := m.Update(fixItemsMsg{items: items, bufID: 1, at: requestedAt, version: requestedVersion})
	if got := res2.(Model); len(got.fixItems) != 0 {
		t.Errorf("fixItems applied despite cursor move: %+v", got.fixItems)
	}

	// Matching buffer + cursor + version → applied.
	res3, _ := base().Update(fixItemsMsg{items: items, bufID: 1, at: requestedAt, version: requestedVersion})
	got := res3.(Model)
	if len(got.fixItems) != 1 || got.fixItems[0].Label != "Replace if/else with max" {
		t.Errorf("fixItems = %+v, want the matching-buffer/cursor/version result applied", got.fixItems)
	}
}

// TestFixItemsIgnoredWhenVersionChangedDespiteSameCursor is a regression
// test: bufID/cursor alone aren't enough — an edit followed by an undo (or
// any edit that nets back to the original cursor position) leaves the
// cursor exactly where the request was made, but the buffer's content
// changed and reverted in between. A stale LSP code action's edits encode
// positions computed against the *original* content; applying them against
// content that merely looks the same again is not guaranteed safe, so the
// response must still be discarded when the buffer's version has moved on.
func TestFixItemsIgnoredWhenVersionChangedDespiteSameCursor(t *testing.T) {
	m := newTestModel("hello\n")
	m.bufID = 1
	m.cursor = document.Pos{Line: 0, Col: 2}
	requestedAt := m.cursor
	requestedVersion := m.buf.Version()

	// An edit (and its undo) advances the buffer's version even though the
	// cursor ends up back at the exact requested position.
	m.buf.Apply(document.Op{Type: document.OpInsert, InsertLine: 0, InsertCol: 2, InsertText: "X"})
	m.buf.Apply(document.Op{Type: document.OpDelete, FromLine: 0, FromCol: 2, ToLine: 0, ToCol: 3})
	if m.buf.Version() == requestedVersion {
		t.Fatal("test setup: buffer version did not advance across the edit/undo")
	}
	if m.cursor != requestedAt {
		t.Fatal("test setup: cursor should still equal requestedAt")
	}

	items := []ClientFixItem{{Label: "stale action"}}
	res, _ := m.Update(fixItemsMsg{items: items, bufID: 1, at: requestedAt, version: requestedVersion})
	got := res.(Model)
	if len(got.fixItems) != 0 {
		t.Errorf("fixItems = %+v, want none — response was computed against a version the buffer has since moved past", got.fixItems)
	}
}
