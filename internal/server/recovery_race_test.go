package server

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/indiejames/indigo/internal/config"
	"github.com/indiejames/indigo/internal/document"
	proto "github.com/indiejames/indigo/internal/proto"
)

// TestFlushCannotRewriteARecoveryFileSaveRemoved: the periodic flush decided
// what to write from a buffer it had looked at before its writes, so a save
// landing in between removed the recovery file and the flush then wrote it
// back. The content was saved and the file said otherwise; the next open
// restored it as "recovered". The flush is parked here at the point it has
// decided to write, while a save runs.
func TestFlushCannotRewriteARecoveryFileSaveRemoved(t *testing.T) {
	s, entry, cl := newOTTestService(t, "saved\n", 1)
	s.recDir = t.TempDir()
	s.cfg = &config.Config{RecoveryMaxBytes: 1 << 20}
	path := entry.buf.Path()
	entry.buf.Apply(document.Op{Type: document.OpInsert, InsertText: "unsaved "})
	rp := recoveryFilePath(s.recDir, path)

	saved := make(chan error, 1)
	flushParked := make(chan struct{})
	s.recoveryWriteHook = func() {
		s.recoveryWriteHook = nil
		close(flushParked)
		// Let the save run to completion if it can. Serialized behind this
		// write, it cannot, and this gives up waiting.
		select {
		case err := <-saved:
			saved <- err
		case <-time.After(time.Second):
		}
	}
	flushed := make(chan struct{})
	go func() {
		s.refreshRecoveryFile(1)
		close(flushed)
	}()
	<-flushParked
	go func() {
		fut, rel := cl.Save(context.Background(), func(p proto.EditorService_save_Params) error {
			p.SetBufferId(1)
			return nil
		})
		_, err := fut.Struct()
		rel()
		saved <- err
	}()
	<-flushed
	if err := <-saved; err != nil {
		t.Fatalf("Save: %v", err)
	}
	if entry.buf.Dirty() {
		t.Fatal("test setup: the save should have left the buffer clean")
	}
	if _, err := os.Stat(rp); err == nil {
		t.Fatal("a recovery file exists for a buffer that has just been saved")
	}
}

// TestSaveAsLeavesNoRecoveryFileUnderTheOldPath: after a SaveAs nothing refers
// to the old path again, so a recovery file left under it is never removed,
// and opening that path later restores it.
func TestSaveAsLeavesNoRecoveryFileUnderTheOldPath(t *testing.T) {
	s, entry, cl := newOTTestService(t, "text\n", 1)
	s.recDir = t.TempDir()
	s.cfg = &config.Config{RecoveryMaxBytes: 1 << 20}
	oldPath := entry.buf.Path()
	entry.buf.Apply(document.Op{Type: document.OpInsert, InsertText: "more "})
	s.refreshRecoveryFile(1)
	if _, err := os.Stat(recoveryFilePath(s.recDir, oldPath)); err != nil {
		t.Fatalf("test setup: no recovery file was written: %v", err)
	}
	newPath := oldPath + ".new"
	fut, rel := cl.SaveAs(context.Background(), func(p proto.EditorService_saveAs_Params) error {
		p.SetBufferId(1)
		return p.SetPath(newPath)
	})
	_, err := fut.Struct()
	rel()
	if err != nil {
		t.Fatal(err)
	}
	s.refreshRecoveryFile(1)
	for _, p := range []string{oldPath, newPath} {
		if _, err := os.Stat(recoveryFilePath(s.recDir, p)); err == nil {
			t.Errorf("a recovery file exists for %s after SaveAs", p)
		}
	}
}
