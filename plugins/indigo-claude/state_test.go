package main

import (
	"testing"
	"time"
)

func TestStateRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // hermetic os.UserConfigDir on darwin/linux
	t.Setenv("XDG_CONFIG_HOME", "")

	workDir := "/some/workspace"
	st := persistedState{
		Version:      stateVersion,
		WorkDir:      workDir,
		Mode:         "cli",
		SavedAt:      time.Now(),
		SessionID:    "sess-123",
		Conv:         []ConvMsg{{Role: RoleUser, Content: "hi"}, {Role: RoleAssistant, Content: "hello"}},
		InputHistory: []string{"hi"},
		CtxTokens:    1234,
	}
	if err := writeState(st); err != nil {
		t.Fatalf("writeState: %v", err)
	}

	got := loadState(workDir, "") // "" = CLI mode
	if got == nil {
		t.Fatal("loadState returned nil for freshly saved state")
	}
	if got.SessionID != "sess-123" || len(got.Conv) != 2 || got.CtxTokens != 1234 {
		t.Errorf("loaded state mismatch: %+v", got)
	}

	// Mode mismatch must not restore (API key set = api mode).
	if loadState(workDir, "sk-ant-xxx") != nil {
		t.Error("loadState restored a CLI-mode state into API mode")
	}

	// A different workspace must not see it.
	if loadState("/other/workspace", "") != nil {
		t.Error("loadState restored state for the wrong workspace")
	}

	// Stale state must be dropped.
	st.SavedAt = time.Now().Add(-8 * 24 * time.Hour)
	if err := writeState(st); err != nil {
		t.Fatalf("writeState stale: %v", err)
	}
	if loadState(workDir, "") != nil {
		t.Error("loadState restored state older than the retention window")
	}

	// deleteState removes it.
	st.SavedAt = time.Now()
	writeState(st) //nolint:errcheck
	deleteState(workDir)
	if loadState(workDir, "") != nil {
		t.Error("state still loadable after deleteState")
	}
}

func TestSnapshotStateClearsRunningTools(t *testing.T) {
	m := Model{
		workDir: "/w",
		prog:    &programLink{},
		conv: []ConvMsg{
			{Role: RoleTool, Content: "  ⟳ Bash (3s)", StartedAt: time.Now()},
		},
	}
	st := snapshotState(m)
	if !st.Conv[0].StartedAt.IsZero() {
		t.Error("snapshot kept a running-tool StartedAt; it would tick forever on restore")
	}
	// The original model must be untouched.
	if m.conv[0].StartedAt.IsZero() {
		t.Error("snapshotState mutated the live model")
	}
}
