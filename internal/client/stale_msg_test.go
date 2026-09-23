package client

import (
	"testing"

	"github.com/indiejames/indigo/internal/document"
)

// These cover the class Phase 6 item 23 deferred: an async LSP result arriving
// after the user switched tabs was applied to whatever buffer is now active.
// None of them corrupts content — which is why they waited — but each is a
// popup, jump or status message attached to the wrong file.

func staleTestModel() Model {
	m := newTestModel("package main\n\nfunc main() {}\n")
	m.rpc = &RPC{}
	m.bufID = 7
	return m
}

func TestHoverIgnoredOnBufferOrCursorChange(t *testing.T) {
	m := staleTestModel()
	m.cursor = document.Pos{Line: 2, Col: 5}

	// Wrong buffer.
	updated, _ := m.Update(hoverMsg{
		result: ClientHoverResult{Found: true, Contents: "from another file"},
		bufID:  9, at: m.cursor,
	})
	if got := updated.(Model).hoverContent; got != nil {
		t.Errorf("hover from another buffer was shown: %q", *got)
	}

	// Right buffer, but the cursor has moved on — the same defect reported for
	// code actions: a popup describing where the request was made, not where the
	// user is now.
	updated, _ = m.Update(hoverMsg{
		result: ClientHoverResult{Found: true, Contents: "stale position"},
		bufID:  m.bufID, at: document.Pos{Line: 0, Col: 0},
	})
	if got := updated.(Model).hoverContent; got != nil {
		t.Errorf("hover for a stale cursor position was shown: %q", *got)
	}

	// Current buffer and position: shown.
	updated, _ = m.Update(hoverMsg{
		result: ClientHoverResult{Found: true, Contents: "current"},
		bufID:  m.bufID, at: m.cursor,
	})
	if got := updated.(Model).hoverContent; got == nil || *got != "current" {
		t.Errorf("a current hover result was discarded: %v", got)
	}
}

func TestSignatureHelpIgnoredOnBufferOrCursorChange(t *testing.T) {
	m := staleTestModel()
	m.cursor = document.Pos{Line: 2, Col: 5}
	help := &ClientSigHelp{}

	updated, _ := m.Update(sigHelpMsg{help: help, bufID: 9, at: m.cursor})
	if updated.(Model).sigHelp != nil {
		t.Error("signature help from another buffer was shown")
	}
	updated, _ = m.Update(sigHelpMsg{help: help, bufID: m.bufID, at: document.Pos{Line: 0, Col: 0}})
	if updated.(Model).sigHelp != nil {
		t.Error("signature help for a stale cursor position was shown")
	}
	updated, _ = m.Update(sigHelpMsg{help: help, bufID: m.bufID, at: m.cursor})
	if updated.(Model).sigHelp == nil {
		t.Error("current signature help was discarded")
	}
}

// TestCompletionsIgnoredOnBufferChangeButNotCursorMove pins the deliberate
// asymmetry. Auto-triggered completions are debounced and the cursor advances
// while the request is in flight, so a strict position check would discard the
// results typing is meant to produce — the handler re-derives the prefix instead.
// A different buffer is still plainly wrong.
func TestCompletionsIgnoredOnBufferChangeButNotCursorMove(t *testing.T) {
	m := staleTestModel()
	m.mode = ModeInsert // completions only ever apply in Insert mode
	m.cursor = document.Pos{Line: 2, Col: 5}
	items := []ClientCompletion{{Label: "main"}}

	updated, _ := m.Update(completionsMsg{items: items, bufID: 9})
	if updated.(Model).completionOn {
		t.Error("completions from another buffer were shown")
	}

	m.cursor = document.Pos{Line: 2, Col: 7} // cursor advanced since the request
	updated, _ = m.Update(completionsMsg{items: items, bufID: m.bufID})
	if len(updated.(Model).completionsRaw) == 0 {
		t.Error("completions were discarded because the cursor advanced; debounced auto-trigger depends on them surviving that")
	}
}

func TestDefinitionIgnoredOnBufferChange(t *testing.T) {
	m := staleTestModel()
	start := m.cursor

	updated, _ := m.Update(definitionMsg{
		loc: ClientLocation{Path: m.filePath, Line: 2, Col: 5}, found: true, bufID: 9,
	})
	if updated.(Model).cursor != start {
		t.Error("a definition result for another buffer moved this buffer's cursor")
	}
}

func TestRenameAndMoveStatusIgnoredOnBufferChange(t *testing.T) {
	m := staleTestModel()

	updated, _ := m.Update(renameSymbolDoneMsg{applied: 3, files: 2, bufID: 9})
	if updated.(Model).status != "" {
		t.Errorf("rename status from another buffer was shown: %q", updated.(Model).status)
	}
	updated, _ = m.Update(moveFunctionDoneMsg{destPath: "/tmp/x.go", bufID: 9})
	if updated.(Model).status != "" {
		t.Errorf("move status from another buffer was shown: %q", updated.(Model).status)
	}
}

// TestDocSymbolsPickerUsesTheRequestedBuffer checks the picker is labelled with
// the buffer the symbols came from.
//
// The handler previously read that id from the model, which mislabelled one
// file's symbols with another's after a tab switch. The guard added above closes
// that too — past it the two ids are equal — so reading msg.bufID is no longer
// independently observable. It is kept because being locally correct beats being
// correct only as long as a check three lines up survives.
func TestDocSymbolsPickerUsesTheRequestedBuffer(t *testing.T) {
	m := staleTestModel()

	updated, cmd := m.Update(docSymbolsMsg{syms: []ClientSymbol{{Name: "main"}}, bufID: 9})
	if updated.(Model).status != "" || cmd != nil {
		t.Error("doc symbols from another buffer were acted on")
	}

	_, cmd = m.Update(docSymbolsMsg{syms: []ClientSymbol{{Name: "main"}}, bufID: m.bufID})
	if cmd == nil {
		t.Fatal("current doc symbols produced no picker")
	}
	open, ok := cmd().(OpenDocSymbolPickerMsg)
	if !ok {
		t.Fatalf("got %T, want OpenDocSymbolPickerMsg", cmd())
	}
	if open.BufID != m.bufID {
		t.Errorf("picker labelled with bufID %d, want %d", open.BufID, m.bufID)
	}
}

// TestCompletionsAfterLeavingInsertModeAreDiscarded is the regression for a
// popup left open in Normal mode, following the cursor around: pressing Esc
// while a completion request was in flight (or before the auto-trigger's
// debounce expired) let the response open the popup after Insert mode had
// ended, and Normal mode has no key that closes it.
func TestCompletionsAfterLeavingInsertModeAreDiscarded(t *testing.T) {
	m := staleTestModel()
	m.mode = ModeInsert
	m.cursor = document.Pos{Line: 2, Col: 5}
	seq := m.completionSeq

	// Esc before the response arrives.
	updated, _ := executeInsertEsc(m)
	m = updated.(Model)
	if m.mode != ModeNormal {
		t.Fatalf("mode = %v after Esc, want Normal", m.mode)
	}

	// "function" matches whatever prefix the cursor is on after Esc ("func" or
	// none), so only the mode check — not the filter — can keep it closed.
	updated, _ = m.Update(completionsMsg{items: []ClientCompletion{{Label: "function"}}, bufID: m.bufID})
	if updated.(Model).completionOn {
		t.Error("a completion list that arrived after Esc opened the popup in Normal mode")
	}

	// The debounced trigger firing after Esc must not start a fetch at all.
	if _, cmd := m.Update(triggerCompletionMsg{seq: seq}); cmd != nil {
		t.Error("a debounced completion trigger fired after Esc and started a fetch")
	}
}

// Leaving Insert mode must not carry an open popup into Normal mode.
func TestInsertEscClearsCompletionPopup(t *testing.T) {
	m := staleTestModel()
	m.mode = ModeInsert
	m.completionOn = true
	m.completions = []ClientCompletion{{Label: "main"}}
	m.completionsRaw = m.completions

	updated, _ := executeInsertEsc(m)
	if got := updated.(Model); got.completionOn || got.completions != nil || got.completionsRaw != nil {
		t.Error("completion popup state survived leaving Insert mode")
	}
}
