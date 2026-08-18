package client

import (
	"testing"

	"github.com/indiejames/indigo/internal/document"
)

// TestOrganizeImportsAppliesEdits verifies a successful organizeImportsMsg
// applies its edits through applyLspEdits (the same undo-aware path every
// other LSP-edit-producing command uses) and reports a status message.
func TestOrganizeImportsAppliesEdits(t *testing.T) {
	m := newTestModel("import (\n\t\"fmt\"\n\t\"os\"\n)\n")
	m.bufID = 1

	edits := []ClientLspEdit{{
		FromLine: 1, FromCol: 0,
		ToLine: 3, ToCol: 0,
		NewText: "\t\"os\"\n",
	}}
	m2, cmd := m.Update(organizeImportsMsg{bufID: 1, edits: edits})
	got := m2.(Model)

	want := "import (\n\t\"os\"\n)\n"
	if got.buf.Content() != want {
		t.Errorf("buf.Content() = %q, want %q", got.buf.Content(), want)
	}
	if got.status != "Organized imports" {
		t.Errorf("status = %q, want %q", got.status, "Organized imports")
	}
	if cmd == nil {
		t.Error("expected a non-nil cmd (applyLspEdits' server-sync/highlight refresh)")
	}
}

// TestOrganizeImportsNoEditsShowsStatus verifies an empty edit list (no
// language server, or nothing to organize) surfaces a status message
// instead of silently doing nothing.
func TestOrganizeImportsNoEditsShowsStatus(t *testing.T) {
	m := newTestModel("package main\n")
	m.bufID = 1

	m2, cmd := m.Update(organizeImportsMsg{bufID: 1, edits: nil})
	got := m2.(Model)

	if got.buf.Content() != "package main\n" {
		t.Errorf("buf.Content() = %q, want unchanged", got.buf.Content())
	}
	if got.status == "" {
		t.Error("expected a non-empty status explaining nothing changed")
	}
	if cmd != nil {
		t.Error("expected a nil cmd when there are no edits to apply")
	}
}

// TestOrganizeImportsErrShowsStatus verifies an RPC failure surfaces as a
// status message rather than being swallowed.
func TestOrganizeImportsErrShowsStatus(t *testing.T) {
	m := newTestModel("package main\n")
	m.bufID = 1

	m2, cmd := m.Update(organizeImportsMsg{bufID: 1, err: errBoom})
	got := m2.(Model)

	if got.status == "" {
		t.Error("expected a non-empty status describing the error")
	}
	if cmd != nil {
		t.Error("expected a nil cmd for an error result")
	}
}

// TestOrganizeImportsDiscardsStaleBufVersion verifies a result whose edits
// were computed against the buffer's content at request time is discarded
// (not applied at now-wrong positions) if the buffer was edited — locally
// or via a remote op — before the response arrived, even though bufID
// still matches (same buffer, just changed underneath the in-flight
// request).
func TestOrganizeImportsDiscardsStaleBufVersion(t *testing.T) {
	m := newTestModel("package main\n")
	m.bufID = 1
	staleVersion := m.buf.Version()

	// Simulate an edit landing on the same buffer while the request was
	// in flight.
	m.buf.Apply(document.Op{Type: document.OpInsert, InsertLine: 0, InsertCol: 0, InsertText: "// edited\n"})

	edits := []ClientLspEdit{{FromLine: 0, FromCol: 0, ToLine: 0, ToCol: 7, NewText: "changed"}}
	m2, cmd := m.Update(organizeImportsMsg{bufID: 1, bufVersion: staleVersion, edits: edits})
	got := m2.(Model)

	want := "// edited\npackage main\n"
	if got.buf.Content() != want {
		t.Errorf("buf.Content() = %q, want %q (stale-position edits must not be applied)", got.buf.Content(), want)
	}
	if got.status == "" {
		t.Error("expected a non-empty status explaining the result was discarded")
	}
	if cmd != nil {
		t.Error("expected a nil cmd for a discarded stale-version result")
	}
}

// TestOrganizeImportsDiscardsStaleBufID verifies a result that arrives after
// the user switched to a different buffer is discarded rather than
// mutating whatever buffer is now active — same guard formatResultMsg has.
func TestOrganizeImportsDiscardsStaleBufID(t *testing.T) {
	m := newTestModel("original content\n")
	m.bufID = 1

	edits := []ClientLspEdit{{FromLine: 0, FromCol: 0, ToLine: 0, ToCol: 7, NewText: "changed"}}
	m2, cmd := m.Update(organizeImportsMsg{bufID: 2, edits: edits})
	got := m2.(Model)

	if got.buf.Content() != "original content\n" {
		t.Errorf("buf.Content() = %q, want the original content untouched (stale result from bufID 2, model is bufID 1)", got.buf.Content())
	}
	if got.status != "" {
		t.Errorf("status = %q, want empty for a discarded stale-bufID result", got.status)
	}
	if cmd != nil {
		t.Error("expected a nil cmd for a discarded stale-bufID result")
	}
}
