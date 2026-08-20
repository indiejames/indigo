package client

import "testing"

// TestDiagsAtPosCoversMultiLineDiagnostic is a regression test: diagsAtPos
// used to check only d.Line != line, ignoring d.EndLine entirely, so a
// multi-line diagnostic (Line < EndLine) — rendered correctly across every
// line by render.go's underline painting — only ever matched on its first
// line. The cursor sitting anywhere else within it (very plausible for a
// several-line diagnostic) found nothing, so the auto-popup silently never
// appeared despite a real diagnostic being visibly underlined.
func TestDiagsAtPosCoversMultiLineDiagnostic(t *testing.T) {
	m := newTestModel("line0\nline1\nline2\nline3\nline4\n")
	m.diagnostics = []ClientDiag{
		{Line: 1, Col: 2, EndLine: 3, EndCol: 4, Message: "spans lines 1-3"},
	}

	// First line: only columns >= Col count.
	if got := m.diagsAtPos(1, 1); len(got) != 0 {
		t.Errorf("diagsAtPos(1, 1) = %+v, want none (before start col on the first line)", got)
	}
	if got := m.diagsAtPos(1, 2); len(got) != 1 {
		t.Errorf("diagsAtPos(1, 2) = %+v, want one match (at start col on the first line)", got)
	}
	if got := m.diagsAtPos(1, 100); len(got) != 1 {
		t.Errorf("diagsAtPos(1, 100) = %+v, want one match (rest of the first line)", got)
	}

	// Interior line: every column counts, including 0 (whitespace/indent).
	if got := m.diagsAtPos(2, 0); len(got) != 1 {
		t.Errorf("diagsAtPos(2, 0) = %+v, want one match (interior line covered in full)", got)
	}

	// Last line: only columns < EndCol count.
	if got := m.diagsAtPos(3, 3); len(got) != 1 {
		t.Errorf("diagsAtPos(3, 3) = %+v, want one match (before end col on the last line)", got)
	}
	if got := m.diagsAtPos(3, 4); len(got) != 0 {
		t.Errorf("diagsAtPos(3, 4) = %+v, want none (at/past end col on the last line)", got)
	}

	// Outside the range entirely.
	if got := m.diagsAtPos(0, 0); len(got) != 0 {
		t.Errorf("diagsAtPos(0, 0) = %+v, want none (before the diagnostic starts)", got)
	}
	if got := m.diagsAtPos(4, 0); len(got) != 0 {
		t.Errorf("diagsAtPos(4, 0) = %+v, want none (after the diagnostic ends)", got)
	}
}

// TestDiagsAtPosSingleLineUnchanged verifies the original single-line
// behavior (Line == EndLine) is unaffected by the multi-line fix.
func TestDiagsAtPosSingleLineUnchanged(t *testing.T) {
	m := newTestModel("hello world\n")
	m.diagnostics = []ClientDiag{
		{Line: 0, Col: 6, EndLine: 0, EndCol: 11, Message: "world"},
	}
	if got := m.diagsAtPos(0, 5); len(got) != 0 {
		t.Errorf("diagsAtPos(0, 5) = %+v, want none (before Col)", got)
	}
	if got := m.diagsAtPos(0, 6); len(got) != 1 {
		t.Errorf("diagsAtPos(0, 6) = %+v, want one match (at Col)", got)
	}
	if got := m.diagsAtPos(0, 10); len(got) != 1 {
		t.Errorf("diagsAtPos(0, 10) = %+v, want one match (last covered col)", got)
	}
	if got := m.diagsAtPos(0, 11); len(got) != 0 {
		t.Errorf("diagsAtPos(0, 11) = %+v, want none (at EndCol)", got)
	}
}
