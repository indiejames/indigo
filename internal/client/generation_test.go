package client

import (
	"testing"

	"github.com/indiejames/indigo/internal/document"
)

// TestUpdatesMsgDetectsGenerationMismatchAndResyncs is a regression test:
// GetUpdates's since>=version check alone can't detect a buffer-object swap
// (format-on-save/SaveAs/DiscardRecovery/Format all reset version to 0 on a
// fresh document.New), so a client could silently miss that its whole
// buffer was replaced and either see no ops or misapply ops describing a
// different buffer object. A generation mismatch must discard the ops and
// trigger a resync instead.
func TestUpdatesMsgDetectsGenerationMismatchAndResyncs(t *testing.T) {
	m := newTestModel("original content\n")
	m.rpc = &RPC{}
	m.generation = 1
	m.generationKnown = true

	op := document.Op{Type: document.OpInsert, InsertLine: 0, InsertCol: 0, InsertText: "x"}
	msg := updatesMsg{ops: []document.Op{op}, version: 5, generation: 2}

	updated, cmd := m.Update(msg)
	m2 := updated.(Model)

	if m2.buf.Content() != "original content\n" {
		t.Errorf("buf.Content() = %q, want unchanged — mismatched-generation ops must not be applied", m2.buf.Content())
	}
	if cmd == nil {
		t.Fatal("expected a non-nil resync command")
	}
	if m2.status == "" {
		t.Error("expected a status message about the resync")
	}
}

// TestUpdatesMsgEstablishesGenerationBaselineOnFirstPoll verifies the first
// updatesMsg a Model ever receives establishes its generation baseline
// rather than being (incorrectly) treated as a mismatch against the zero
// value.
func TestUpdatesMsgEstablishesGenerationBaselineOnFirstPoll(t *testing.T) {
	m := newTestModel("hello\n")
	m.rpc = &RPC{}
	if m.generationKnown {
		t.Fatal("test setup: generationKnown should start false")
	}

	msg := updatesMsg{version: 3, generation: 7}
	updated, _ := m.Update(msg)
	m2 := updated.(Model)

	if !m2.generationKnown || m2.generation != 7 {
		t.Errorf("generation = %d, generationKnown = %v, want (7, true)", m2.generation, m2.generationKnown)
	}
	if m2.buf.Content() != "hello\n" {
		t.Errorf("buf.Content() = %q, want unchanged (no ops in this update)", m2.buf.Content())
	}
}

// TestUpdatesMsgAppliesOpsWhenGenerationMatches is the happy path: a
// matching generation applies the incoming ops as before.
func TestUpdatesMsgAppliesOpsWhenGenerationMatches(t *testing.T) {
	m := newTestModel("hello\n")
	m.rpc = &RPC{}
	m.generation = 5
	m.generationKnown = true

	op := document.Op{Type: document.OpInsert, InsertLine: 0, InsertCol: 5, InsertText: " world"}
	msg := updatesMsg{ops: []document.Op{op}, version: 9, generation: 5}

	updated, _ := m.Update(msg)
	m2 := updated.(Model)

	if m2.buf.Content() != "hello world\n" {
		t.Errorf("buf.Content() = %q, want %q", m2.buf.Content(), "hello world\n")
	}
	if m2.version != 9 {
		t.Errorf("version = %d, want 9", m2.version)
	}
}
