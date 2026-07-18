package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestAutoApproveSlashCommand(t *testing.T) {
	m := newModel(nil, &programLink{}, "", t.TempDir())

	m = submitText(m, "/autoapprove on")
	edits, shell := m.prog.autoApprove()
	if !edits || !shell {
		t.Fatalf("after /autoapprove on: edits=%v shell=%v, want both true", edits, shell)
	}

	m = submitText(m, "/autoapprove off")
	edits, shell = m.prog.autoApprove()
	if edits || shell {
		t.Fatalf("after /autoapprove off: edits=%v shell=%v, want both false", edits, shell)
	}

	m = submitText(m, "/autoapprove edits on")
	edits, shell = m.prog.autoApprove()
	if !edits || shell {
		t.Fatalf("after /autoapprove edits on: edits=%v shell=%v, want edits=true shell=false", edits, shell)
	}

	m = submitText(m, "/autoapprove shell on")
	edits, shell = m.prog.autoApprove()
	if !edits || !shell {
		t.Fatalf("after /autoapprove shell on: edits=%v shell=%v, want both true", edits, shell)
	}

	m = submitText(m, "/autoapprove edits off")
	edits, shell = m.prog.autoApprove()
	if edits || !shell {
		t.Fatalf("after /autoapprove edits off: edits=%v shell=%v, want edits=false shell=true", edits, shell)
	}
}

func TestAutoApproveSlashCommandClearsInputAndShowsStatus(t *testing.T) {
	m := newModel(nil, &programLink{}, "", t.TempDir())
	m = submitText(m, "/autoapprove on")
	if len(m.input) != 0 {
		t.Errorf("input not cleared after /autoapprove: %q", string(m.input))
	}
	last := m.conv[len(m.conv)-1]
	if last.Role != RoleStatus {
		t.Fatalf("last message role = %v, want RoleStatus", last.Role)
	}
}

func TestRequestEditApprovalSkipsPopupWhenAutoApproved(t *testing.T) {
	prog := &programLink{}
	prog.setAutoApproveEdits(true)
	// send is left nil: if requestEditApproval tried to emit+block anyway,
	// this would hang forever waiting on a reply nothing will ever send.
	if !requestEditApproval(prog, permissionRequestMsg{file: "x.go"}) {
		t.Error("requestEditApproval() = false, want true when auto-approve is on")
	}
}

func TestRequestEditApprovalRoundTripsWhenNotAutoApproved(t *testing.T) {
	for _, want := range []bool{true, false} {
		prog := &programLink{}
		prog.send = func(msg tea.Msg) {
			if r, ok := msg.(permissionRequestMsg); ok {
				r.replyCh <- want
			}
		}
		got := requestEditApproval(prog, permissionRequestMsg{file: "x.go"})
		if got != want {
			t.Errorf("requestEditApproval() = %v, want %v (round-tripped through the popup)", got, want)
		}
	}
}

func TestRenderHeaderShowsAutoApproveIndicator(t *testing.T) {
	m := newModel(nil, &programLink{}, "", t.TempDir())
	m.width = 100

	if got := m.renderHeader(); strings.Contains(got, "auto-approve") {
		t.Errorf("header shows auto-approve indicator when it's off: %q", got)
	}

	m.prog.setAutoApproveEdits(true)
	if got := m.renderHeader(); !strings.Contains(got, "auto-approve: edits") {
		t.Errorf("header missing auto-approve: edits indicator: %q", got)
	}

	m.prog.setAutoApproveShell(true)
	if got := m.renderHeader(); !strings.Contains(got, "auto-approve: all") {
		t.Errorf("header missing auto-approve: all indicator: %q", got)
	}
}
