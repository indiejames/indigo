package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// ─── session persistence ──────────────────────────────────────────────────────
//
// The conversation survives restarts: after every completed turn (and on quit)
// the display transcript plus the resume handle is written to a per-workspace
// state file. On startup the state is restored if it is recent enough.
//
// CLI mode persists the sessionID (Claude Code keeps the full transcript in
// ~/.claude/projects/ and --resume restores it); API mode persists the full
// message history since it lives only in this process.

// stateRetention is how long a saved conversation stays restorable.
const stateRetention = 7 * 24 * time.Hour

const stateVersion = 1

type persistedState struct {
	Version      int          `json:"version"`
	WorkDir      string       `json:"work_dir"`
	Mode         string       `json:"mode"` // "cli" or "api"
	SavedAt      time.Time    `json:"saved_at"`
	SessionID    string       `json:"session_id,omitempty"`
	Conv         []ConvMsg    `json:"conv,omitempty"`
	History      []apiMessage `json:"history,omitempty"`
	InputHistory []string     `json:"input_history,omitempty"`
	CtxTokens    int          `json:"ctx_tokens,omitempty"`
}

func modeName(apiKey string) string {
	if apiKey != "" {
		return "api"
	}
	return "cli"
}

// statePath returns the per-workspace state file path, creating parent dirs.
func statePath(workDir string) (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "indigo-claude", "sessions")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	h := sha256.Sum256([]byte(workDir))
	return filepath.Join(dir, fmt.Sprintf("%x.json", h[:8])), nil
}

// snapshotState captures everything worth persisting from the model. Called on
// the Update goroutine so the copies are race-free; the write happens later.
func snapshotState(m Model) persistedState {
	conv := make([]ConvMsg, len(m.conv))
	copy(conv, m.conv)
	for i := range conv {
		// Never persist a running-tool spinner; it would tick forever on restore.
		conv[i].StartedAt = time.Time{}
	}
	hist := make([]apiMessage, len(m.history))
	copy(hist, m.history)
	inputs := make([]string, len(m.inputHistory))
	copy(inputs, m.inputHistory)
	return persistedState{
		Version:      stateVersion,
		WorkDir:      m.workDir,
		Mode:         modeName(m.apiKey),
		SavedAt:      time.Now(),
		SessionID:    m.sessionID,
		Conv:         conv,
		History:      hist,
		InputHistory: inputs,
		CtxTokens:    m.ctxTokens,
	}
}

// writeState atomically writes the state file (temp + rename).
func writeState(st persistedState) error {
	path, err := statePath(st.WorkDir)
	if err != nil {
		return err
	}
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// saveStateCmd snapshots the model now and writes the file off the UI loop.
func (m Model) saveStateCmd() tea.Cmd {
	st := snapshotState(m)
	return func() tea.Msg {
		writeState(st) //nolint:errcheck
		return nil
	}
}

// loadState returns the saved state for workDir, or nil when there is none,
// it is stale, it belongs to a different mode, or it fails to parse.
func loadState(workDir, apiKey string) *persistedState {
	path, err := statePath(workDir)
	if err != nil {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var st persistedState
	if err := json.Unmarshal(data, &st); err != nil {
		return nil
	}
	if st.Version != stateVersion || st.WorkDir != workDir || st.Mode != modeName(apiKey) {
		return nil
	}
	if time.Since(st.SavedAt) > stateRetention {
		os.Remove(path) //nolint:errcheck
		return nil
	}
	if len(st.Conv) == 0 {
		return nil
	}
	return &st
}

// deleteState removes the saved conversation (used by /clear).
func deleteState(workDir string) {
	if path, err := statePath(workDir); err == nil {
		os.Remove(path) //nolint:errcheck
	}
}

// restoreState merges a saved conversation into a freshly constructed model.
func (m Model) restoreState(st *persistedState) Model {
	if st == nil {
		return m
	}
	m.conv = append(st.Conv, ConvMsg{Role: RoleStatus, Content: fmt.Sprintf(
		"Restored conversation from %s — /clear to start fresh.",
		st.SavedAt.Format("Jan 2 15:04"))})
	m.sessionID = st.SessionID
	m.history = st.History
	m.inputHistory = st.InputHistory
	m.ctxTokens = st.CtxTokens
	return m
}
