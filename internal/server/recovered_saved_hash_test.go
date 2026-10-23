package server

import (
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/indiejames/indigo/internal/config"
	"github.com/indiejames/indigo/internal/format"
	"github.com/indiejames/indigo/internal/lint"
	"github.com/indiejames/indigo/internal/lsp"
	"github.com/indiejames/indigo/internal/plugin"
	"github.com/indiejames/indigo/internal/proto"
)

// TestARecoveredBufferIsStillUnsaved: a buffer opened from a recovery file
// holds edits the file on disk does not have, so the "last saved" content
// every poll reports must be the file's, not the recovered text.
//
// It was the recovered text. A window compares its content with that hash on
// every poll and, finding them equal, cleared its unsaved marker — so restored
// unsaved work showed as saved within a poll of opening. Found when a dev
// container window reconnected to a server that had restored its buffer from
// the recovery file written on the drop.
func TestARecoveredBufferIsStillUnsaved(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	const disk, recovered = "on disk\n", "on disk, plus unsaved edits\n"
	if err := os.WriteFile(path, []byte(disk), 0o644); err != nil {
		t.Fatal(err)
	}
	recDir := t.TempDir()
	recPath := recoveryFilePath(recDir, path)
	if err := os.WriteFile(recPath, []byte(recovered), 0o600); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Minute) // newer than the file, so it is used
	if err := os.Chtimes(recPath, future, future); err != nil {
		t.Fatal(err)
	}

	s := &editorService{
		cfg:        &config.Config{},
		buffers:    map[uint32]*bufferEntry{},
		fmtMgr:     format.NewManager(nil, &config.Config{}, dir),
		lspMgr:     lsp.NewManager(dir, nil),
		lintMgr:    &lint.Manager{},
		pluginMgr:  &plugin.Manager{},
		dirWatches: make(map[string]int),
		recDir:     recDir,
	}
	cl := proto.EditorService_ServerToClient(&connSvc{editorService: s, connID: 1})
	defer cl.Release()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ofFut, ofRel := cl.OpenFile(ctx, func(p proto.EditorService_openFile_Params) error {
		p.SetClientId(1)
		return p.SetPath(path)
	})
	defer ofRel()
	ofRes, err := ofFut.Struct()
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	if !ofRes.FromRecovery() {
		t.Fatal("the recovery file was not used; the test proves nothing")
	}

	upFut, upRel := cl.GetUpdates(ctx, func(p proto.EditorService_getUpdates_Params) error {
		p.SetClientId(1)
		p.SetBufferId(ofRes.BufferId())
		return nil
	})
	defer upRel()
	upRes, err := upFut.Struct()
	if err != nil {
		t.Fatalf("GetUpdates: %v", err)
	}
	got, _ := upRes.SavedHash()
	if want := sha256.Sum256([]byte(disk)); string(got) != string(want[:]) {
		if rec := sha256.Sum256([]byte(recovered)); string(got) == string(rec[:]) {
			t.Fatal("saved hash is the recovered text's, so a window shows restored unsaved edits as saved")
		}
		t.Fatal("saved hash is not the file's on disk")
	}
}
