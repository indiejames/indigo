package server

import (
	"context"
	"crypto/sha256"
	"path/filepath"
	"testing"
	"time"

	"github.com/indiejames/indigo/internal/config"
	"github.com/indiejames/indigo/internal/document"
	proto "github.com/indiejames/indigo/internal/proto"
)

// TestGetSyncStateReportsBookkeeping checks the projection reports what a
// diagnosis actually needs: per-buffer version/generation/dirty/hash, and each
// attached client's acknowledged version — the value that distinguishes "this
// window is up to date" from "this window stopped consuming ops and is showing
// stale content while also blocking history trimming".
func TestGetSyncStateReportsBookkeeping(t *testing.T) {
	dir := t.TempDir()
	pathA := filepath.Join(dir, "a.go")
	pathB := filepath.Join(dir, "b.go")
	contentA := "package main\n"

	bufA := document.New(pathA, contentA)
	s := &editorService{
		cfg: &config.Config{},
		buffers: map[uint32]*bufferEntry{
			2: {
				buf:           document.New(pathB, "b\n"),
				canonPath:     canonicalPath(pathB),
				clients:       map[uint64]struct{}{7: {}},
				sinceByClient: map[uint64]uint64{7: 0},
			},
			1: {
				buf:        bufA,
				canonPath:  canonicalPath(pathA),
				generation: 3,
				clients:    map[uint64]struct{}{1: {}, 2: {}},
				// Client 2 is behind: it has acknowledged nothing.
				sinceByClient: map[uint64]uint64{1: 0, 2: 0},
			},
		},
		clientMap: map[uint64]*clientEntry{
			1: {connID: 11},
			2: {connID: 22},
		},
		dirWatches:  make(map[string]int),
		savingPaths: make(map[string]time.Time),
	}
	// Advance buffer A so client 1 can be caught up and client 2 behind.
	bufA.Apply(document.Op{ClientID: 1, Type: document.OpInsert, InsertLine: 0, InsertCol: 0, InsertText: "x"})
	s.buffers[1].sinceByClient[1] = bufA.Version()

	cl := proto.EditorService_ServerToClient(&connSvc{editorService: s, connID: 1})
	defer cl.Release()

	fut, rel := cl.GetSyncState(context.Background(), func(p proto.EditorService_getSyncState_Params) error {
		p.SetBufferId(0) // every buffer
		return nil
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		t.Fatalf("GetSyncState: %v", err)
	}
	list, err := res.Buffers()
	if err != nil {
		t.Fatalf("Buffers: %v", err)
	}
	if list.Len() != 2 {
		t.Fatalf("got %d buffers, want 2", list.Len())
	}

	// Sorted by buffer id, so rows don't shuffle between calls.
	if list.At(0).BufferId() != 1 || list.At(1).BufferId() != 2 {
		t.Errorf("buffer ids = [%d %d], want them sorted [1 2]", list.At(0).BufferId(), list.At(1).BufferId())
	}

	a := list.At(0)
	if got, _ := a.Path(); got != pathA {
		t.Errorf("path = %q, want %q", got, pathA)
	}
	if a.Generation() != 3 {
		t.Errorf("generation = %d, want 3", a.Generation())
	}
	if a.Version() != bufA.Version() {
		t.Errorf("version = %d, want %d", a.Version(), bufA.Version())
	}
	if !a.Dirty() {
		t.Error("dirty = false, want true after an applied op")
	}
	want := sha256.Sum256([]byte("xpackage main\n"))
	got, _ := a.ContentSha256()
	if string(got) != string(want[:]) {
		t.Errorf("content sha256 mismatch")
	}
	if a.ContentBytes() != uint64(len("xpackage main\n")) {
		t.Errorf("contentBytes = %d, want %d", a.ContentBytes(), len("xpackage main\n"))
	}

	clients, err := a.Clients()
	if err != nil {
		t.Fatalf("Clients: %v", err)
	}
	if clients.Len() != 2 {
		t.Fatalf("got %d clients, want 2", clients.Len())
	}
	if clients.At(0).ClientId() != 1 || clients.At(1).ClientId() != 2 {
		t.Errorf("client ids = [%d %d], want them sorted [1 2]",
			clients.At(0).ClientId(), clients.At(1).ClientId())
	}
	if clients.At(0).AckedVersion() != bufA.Version() {
		t.Errorf("caught-up client acked = %d, want %d", clients.At(0).AckedVersion(), bufA.Version())
	}
	if clients.At(1).AckedVersion() != 0 {
		t.Errorf("lagging client acked = %d, want 0", clients.At(1).AckedVersion())
	}
	if clients.At(0).ConnId() != 11 || clients.At(1).ConnId() != 22 {
		t.Errorf("conn ids = [%d %d], want [11 22]", clients.At(0).ConnId(), clients.At(1).ConnId())
	}
}

// TestGetSyncStateFiltersByBufferID covers the single-buffer form.
func TestGetSyncStateFiltersByBufferID(t *testing.T) {
	dir := t.TempDir()
	s := &editorService{
		cfg: &config.Config{},
		buffers: map[uint32]*bufferEntry{
			1: {buf: document.New(filepath.Join(dir, "a.go"), "a\n"), clients: map[uint64]struct{}{}},
			2: {buf: document.New(filepath.Join(dir, "b.go"), "b\n"), clients: map[uint64]struct{}{}},
		},
		clientMap:   map[uint64]*clientEntry{},
		dirWatches:  make(map[string]int),
		savingPaths: make(map[string]time.Time),
	}
	cl := proto.EditorService_ServerToClient(&connSvc{editorService: s, connID: 1})
	defer cl.Release()

	fut, rel := cl.GetSyncState(context.Background(), func(p proto.EditorService_getSyncState_Params) error {
		p.SetBufferId(2)
		return nil
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		t.Fatalf("GetSyncState: %v", err)
	}
	list, _ := res.Buffers()
	if list.Len() != 1 || list.At(0).BufferId() != 2 {
		t.Fatalf("got %d buffers (first id %d), want just buffer 2", list.Len(), list.At(0).BufferId())
	}
}
