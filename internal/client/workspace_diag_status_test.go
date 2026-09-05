package client

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

// --- workspaceDiagSummaryMsg / Update ---

func TestWorkspaceDiagSummaryMsgUpdatesModel(t *testing.T) {
	m := newTestModel("hello\n")
	summary := WorkspaceDiagnosticsSummary{ErrorCount: 2, WarningCount: 1, InfoCount: 0, FileCount: 3}
	updated, cmd := m.Update(workspaceDiagSummaryMsg{summary: summary})
	m = updated.(Model)
	if m.workspaceDiagSummary != summary {
		t.Errorf("workspaceDiagSummary = %+v, want %+v", m.workspaceDiagSummary, summary)
	}
	if cmd != nil {
		t.Error("expected no follow-up command")
	}
}

// --- renderWorkspaceDiagSegment / renderStatusBar ---

func TestRenderWorkspaceDiagSegmentBlankWhenClean(t *testing.T) {
	m := newTestModel("hello\n")
	m.filePath = "test.go"
	seg := m.renderWorkspaceDiagSegment()
	if strings.TrimSpace(ansiStrip(seg)) != "" {
		t.Errorf("clean workspace segment should render blank, got %q", ansiStrip(seg))
	}
}

func TestRenderWorkspaceDiagSegmentShowsErrorCount(t *testing.T) {
	m := newTestModel("hello\n")
	m.filePath = "test.go"
	m.workspaceDiagSummary = WorkspaceDiagnosticsSummary{ErrorCount: 3, WarningCount: 1}
	seg := m.renderWorkspaceDiagSegment()
	stripped := ansiStrip(seg)
	if !strings.Contains(stripped, "4") {
		t.Errorf("workspace segment should show total count 4, got %q", stripped)
	}
}

func TestRenderWorkspaceDiagSegmentFixedWidth(t *testing.T) {
	m := newTestModel("hello\n")
	blank := lipgloss.Width(m.renderWorkspaceDiagSegment())
	m.workspaceDiagSummary = WorkspaceDiagnosticsSummary{ErrorCount: 5}
	withCount := lipgloss.Width(m.renderWorkspaceDiagSegment())
	if blank != withCount {
		t.Errorf("workspace segment width should be fixed: blank=%d withCount=%d", blank, withCount)
	}
}

func TestRenderStatusBarIncludesWorkspaceDiagSummary(t *testing.T) {
	m := newTestModel("hello\n")
	m.filePath = "test.go"
	m.workspaceDiagSummary = WorkspaceDiagnosticsSummary{ErrorCount: 2}
	bar := m.renderStatusBar()
	w := lipgloss.Width(bar)
	if w != m.width {
		t.Errorf("statusBar width = %d, want %d", w, m.width)
	}
	stripped := ansiStrip(bar)
	if !strings.Contains(stripped, "WS") {
		t.Errorf("status bar should contain workspace summary label 'WS': %q", stripped)
	}
}
