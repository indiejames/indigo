package app

import (
	"testing"

	"github.com/indiejames/indigo/internal/client"
	"github.com/indiejames/indigo/internal/config"
)

func newReloadTestModel(bufID uint32, absPath string) client.Model {
	return client.New(&client.RPC{}, bufID, "", 0, absPath, "/tmp", nil, false, 0)
}

// TestBufferReloadedMsgAppliesOnMatchingBufID is the happy path: idx still
// points at the buffer the reload was started for.
func TestBufferReloadedMsgAppliesOnMatchingBufID(t *testing.T) {
	a := App{
		buffers: []client.Model{newReloadTestModel(1, "/tmp/a.go")},
		active:  0,
		width:   80,
		height:  24,
		cfg:     &config.Config{},
	}
	newModel := newReloadTestModel(2, "/tmp/a.go") // reload always gets a fresh bufID (CloseBuffer+OpenFile)
	msg := bufferReloadedMsg{idx: 0, oldBufID: 1, model: newModel}

	updated, cmd := a.Update(msg)
	a2 := updated.(App)

	if a2.buffers[0].BufID() != 2 {
		t.Errorf("buffers[0].BufID() = %d, want 2 (reloaded model applied)", a2.buffers[0].BufID())
	}
	if cmd == nil {
		t.Error("expected a non-nil command (model.Init()) on the happy path")
	}
}

// TestBufferReloadedMsgIgnoredOnStaleIndex is a regression test: idx alone
// isn't a safe identity check — doReloadBuffer's CloseBuffer+OpenFile round
// trip can take up to 5s, during which closing an earlier tab shifts every
// later index down. Before this fix, bufferReloadedMsg only checked idx
// bounds, so it could silently overwrite whatever unrelated buffer now sits
// at that index with a reload result for a completely different file.
func TestBufferReloadedMsgIgnoredOnStaleIndex(t *testing.T) {
	a := App{
		buffers: []client.Model{newReloadTestModel(5, "/tmp/other.go")}, // a different buffer now occupies idx 0
		active:  0,
		width:   80,
		height:  24,
		cfg:     &config.Config{},
	}
	// This reload was originally started against bufID 1 (some buffer that
	// has since closed and been removed from a.buffers).
	msg := bufferReloadedMsg{idx: 0, oldBufID: 1, model: newReloadTestModel(2, "/tmp/a.go")}

	updated, cmd := a.Update(msg)
	a2 := updated.(App)

	if a2.buffers[0].BufID() != 5 || a2.buffers[0].FilePath() != "/tmp/other.go" {
		t.Errorf("buffers[0] = {BufID:%d, FilePath:%q}, want unchanged {5, /tmp/other.go} (stale oldBufID)",
			a2.buffers[0].BufID(), a2.buffers[0].FilePath())
	}
	// The reload already opened bufID 2 server-side; the handler must still
	// return a command to close it rather than leaking it.
	if cmd == nil {
		t.Error("expected a non-nil command to close the now-orphaned reloaded buffer")
	}
}

// TestBufferReloadedMsgIgnoredOnOutOfRangeIndex covers the tab having closed
// entirely (idx no longer exists at all) rather than being reused.
func TestBufferReloadedMsgIgnoredOnOutOfRangeIndex(t *testing.T) {
	a := App{
		buffers: []client.Model{newReloadTestModel(5, "/tmp/other.go")},
		active:  0,
		width:   80,
		height:  24,
		cfg:     &config.Config{},
	}
	msg := bufferReloadedMsg{idx: 3, oldBufID: 1, model: newReloadTestModel(2, "/tmp/a.go")}

	updated, cmd := a.Update(msg)
	a2 := updated.(App)

	if len(a2.buffers) != 1 || a2.buffers[0].BufID() != 5 {
		t.Errorf("buffers mutated on an out-of-range idx: %+v", a2.buffers)
	}
	if cmd == nil {
		t.Error("expected a non-nil command to close the now-orphaned reloaded buffer")
	}
}
