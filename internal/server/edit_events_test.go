package server

import (
	"sync"
	"testing"
	"time"

	"github.com/indiejames/indigo/internal/document"
	"github.com/indiejames/indigo/internal/plugin"
)

type editEventLog struct {
	mu     sync.Mutex
	ranges []plugin.EditRange
	paths  []string
}

func (l *editEventLog) record(bufID uint32, path string, r plugin.EditRange) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if bufID == 1 {
		l.ranges = append(l.ranges, r)
		l.paths = append(l.paths, path)
	}
}

func (l *editEventLog) waitFor(t *testing.T, n int) []plugin.EditRange {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		l.mu.Lock()
		got := append([]plugin.EditRange(nil), l.ranges...)
		l.mu.Unlock()
		if len(got) >= n || time.Now().After(deadline) {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func newEditEventService(t *testing.T, content string) (*editorService, *bufferEntry, *editEventLog, func(document.Op)) {
	t.Helper()
	s, entry, cl := newOTTestService(t, content, 1)
	// As OpenFile wires a real entry.
	entry.id, entry.onApplied = 1, s.afterApplyLocked
	log := &editEventLog{}
	s.editEventSink = log.record
	send := func(op document.Op) {
		t.Helper()
		if err := sendOp(t, cl, 1, entry.buf.Version(), op); err != nil {
			t.Fatal(err)
		}
	}
	return s, entry, log, send
}

// TestServerEditsTellPluginsAboutLineChanges is the regression test for edits
// the server makes itself sending plugins no edit event: only ApplyOp, ApplyOps
// and formatting did, so a plugin edit, a rename or moving text to a file that
// added lines left every bookmark below it on the wrong line.
func TestServerEditsTellPluginsAboutLineChanges(t *testing.T) {
	s, entry, log, _ := newEditEventService(t, "one\ntwo\n")
	if err := s.PluginApplyEdit(1, 0, 0, false, []plugin.TextEdit{{
		FromLine: 1, FromCol: 0, ToLine: 1, ToCol: 0, NewText: "inserted\n",
	}}); err != nil {
		t.Fatalf("PluginApplyEdit: %v", err)
	}
	got := log.waitFor(t, 1)
	want := plugin.EditRange{FromLine: 1, FromCol: 0, ToLine: 1, ToCol: 0, LineDelta: 1}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("events = %+v, want one %+v", got, want)
	}
	if log.paths[0] != entry.buf.Path() {
		t.Errorf("event path = %q, want %q", log.paths[0], entry.buf.Path())
	}
}

// TestWindowEditsTellPluginsInOrder covers the client path: a join then a line
// opened above, sent as two ApplyOps, reach the plugin side as two events in
// that order and with their columns.
func TestWindowEditsTellPluginsInOrder(t *testing.T) {
	_, _, log, send := newEditEventService(t, "zero\none\ntwo\n")
	send(document.Op{Type: document.OpDelete, FromLine: 1, FromCol: 3, ToLine: 2, ToCol: 0})  // J on line 1
	send(document.Op{Type: document.OpInsert, InsertLine: 1, InsertCol: 0, InsertText: "\n"}) // O on line 1
	got := log.waitFor(t, 2)
	want := []plugin.EditRange{
		{FromLine: 1, FromCol: 3, ToLine: 2, ToCol: 0, LineDelta: -1},
		{FromLine: 1, FromCol: 0, ToLine: 1, ToCol: 0, LineDelta: 1},
	}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("events = %+v, want %+v", got, want)
	}
}
