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

	// Different buffer → ignored.
	res, _ := base().Update(fixItemsMsg{items: items, bufID: 2, at: requestedAt})
	if got := res.(Model); len(got.fixItems) != 0 {
		t.Errorf("fixItems applied despite bufID mismatch: %+v", got.fixItems)
	}

	// Cursor moved since the request → ignored.
	m := base()
	m.cursor = document.Pos{Line: 0, Col: 4}
	res2, _ := m.Update(fixItemsMsg{items: items, bufID: 1, at: requestedAt})
	if got := res2.(Model); len(got.fixItems) != 0 {
		t.Errorf("fixItems applied despite cursor move: %+v", got.fixItems)
	}

	// Matching buffer + cursor → applied.
	res3, _ := base().Update(fixItemsMsg{items: items, bufID: 1, at: requestedAt})
	got := res3.(Model)
	if len(got.fixItems) != 1 || got.fixItems[0].Label != "Replace if/else with max" {
		t.Errorf("fixItems = %+v, want the matching-buffer/cursor result applied", got.fixItems)
	}
}
