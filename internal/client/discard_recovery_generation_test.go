package client

import (
	"strings"
	"testing"
)

// TestDiscardRecoveryAdoptsGeneration is a regression test: the server bumps the
// buffer's generation when DiscardRecovery swaps in the on-disk content, but the
// RPC didn't return it and this handler didn't adopt it — so m.generation stayed
// stale and the very next updatesMsg poll saw a mismatch.
func TestDiscardRecoveryAdoptsGeneration(t *testing.T) {
	m := newTestModel("recovered content\n")
	m.rpc = &RPC{}
	m.generation = 4
	m.generationKnown = true

	updated, _ := m.Update(discardRecoveryMsg{
		bufID:      m.bufID,
		version:    m.buf.Version(),
		content:    "on disk\n",
		generation: 5,
	})
	m2 := updated.(Model)

	if m2.buf.Content() != "on disk\n" {
		t.Fatalf("buf.Content() = %q, want the on-disk content", m2.buf.Content())
	}
	if m2.generation != 5 || !m2.generationKnown {
		t.Errorf("generation = %d, generationKnown = %v, want (5, true)", m2.generation, m2.generationKnown)
	}
}

// TestDiscardRecoveryDoesNotTriggerSpuriousResync is the symptom the fix exists
// for, driven end to end: after discarding recovery, the next poll carries the
// server's post-swap generation, and with it adopted that poll must be an
// ordinary no-op rather than a resync. The resync is not merely wasted work —
// resyncFromServer marks the buffer dirty, so it left a buffer that had just
// been reset to exactly what is on disk showing unsaved changes.
func TestDiscardRecoveryDoesNotTriggerSpuriousResync(t *testing.T) {
	m := newTestModel("recovered content\n")
	m.rpc = &RPC{}
	m.generation = 4
	m.generationKnown = true

	updated, _ := m.Update(discardRecoveryMsg{
		bufID:      m.bufID,
		version:    m.buf.Version(),
		content:    "on disk\n",
		generation: 5,
	})
	m2 := updated.(Model)
	if m2.Dirty() {
		t.Fatal("buffer is dirty straight after discarding recovery; it matches disk exactly")
	}

	// The next poll, reporting the server's post-swap state.
	updated2, cmd := m2.Update(updatesMsg{bufID: m2.bufID, version: 0, generation: 5})
	m3 := updated2.(Model)

	if cmd != nil {
		t.Error("the poll after discarding recovery produced a command (a resync); the generation was adopted, so there is nothing to resync")
	}
	if strings.Contains(strings.ToLower(m3.status), "resync") {
		t.Errorf("status = %q, want no resync message", m3.status)
	}
	if m3.Dirty() {
		t.Error("buffer became dirty after the poll; a spurious resync marked a just-reset-to-disk buffer as unsaved")
	}
}
