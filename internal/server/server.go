package server

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	capnp "capnproto.org/go/capnp/v3"
	"capnproto.org/go/capnp/v3/rpc"

	"github.com/indiejames/indigo/internal/config"
	"github.com/indiejames/indigo/internal/document"
	proto "github.com/indiejames/indigo/internal/proto"
)

// SocketPath returns the Unix socket path for a given working directory.
func SocketPath(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = dir
	}
	h := sha256.Sum256([]byte(abs))
	return filepath.Join(os.TempDir(), fmt.Sprintf("indigo-%x.sock", h[:8]))
}

// IsRunning returns true if a server socket exists and is accepting connections.
func IsRunning(socketPath string) bool {
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// bufferEntry holds a buffer and tracks which clients have it open.
type bufferEntry struct {
	buf     *document.Buffer
	clients map[uint64]struct{}
}

// editorService implements proto.EditorService_Server.
type editorService struct {
	mu      sync.Mutex
	buffers map[uint32]*bufferEntry
	nextBuf uint32
	clients map[uint64]struct{}
	nextClt uint64
	recDir  string

	// shutdown is called when the last client disconnects cleanly.
	shutdown func()
}

func newEditorService(recDir string, shutdown func()) *editorService {
	return &editorService{
		buffers:  make(map[uint32]*bufferEntry),
		clients:  make(map[uint64]struct{}),
		recDir:   recDir,
		shutdown: shutdown,
	}
}

// recoveryFilePath returns the path for the recovery file for a given source file.
func recoveryFilePath(recDir, filePath string) string {
	h := sha256.Sum256([]byte(filePath))
	return filepath.Join(recDir, fmt.Sprintf("%x.recover", h[:]))
}

// setupRecoveryDir returns (creating if necessary) ~/.indigo/recovery.
func setupRecoveryDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".indigo", "recovery")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	return dir, nil
}

func (s *editorService) Connect(_ context.Context, call proto.EditorService_connect) error {
	res, err := call.AllocResults()
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.nextClt++
	id := s.nextClt
	s.clients[id] = struct{}{}
	s.mu.Unlock()
	res.SetClientId(id)
	return nil
}

func (s *editorService) Disconnect(_ context.Context, call proto.EditorService_disconnect) error {
	clientID := call.Args().ClientId()
	s.mu.Lock()
	delete(s.clients, clientID)
	// Remove client from any open buffers.
	for _, e := range s.buffers {
		delete(e.clients, clientID)
	}
	remaining := len(s.clients)
	s.mu.Unlock()

	if remaining == 0 {
		s.shutdown()
	}
	_, err := call.AllocResults()
	return err
}

func (s *editorService) OpenFile(_ context.Context, call proto.EditorService_openFile) error {
	args := call.Args()
	clientID := args.ClientId()
	path, err := args.Path()
	if err != nil {
		return err
	}

	content, fromRecovery := s.loadContent(path)

	s.mu.Lock()
	// Check if file is already open.
	for id, e := range s.buffers {
		if e.buf.Path() == path {
			e.clients[clientID] = struct{}{}
			ver := e.buf.Version()
			s.mu.Unlock()

			res, err := call.AllocResults()
			if err != nil {
				return err
			}
			res.SetBufferId(id)
			if err := res.SetContent(e.buf.Content()); err != nil {
				return err
			}
			res.SetVersion(ver)
			return nil
		}
	}

	s.nextBuf++
	bufID := s.nextBuf
	buf := document.New(path, content)
	if fromRecovery {
		buf.MarkDirty()
	}
	s.buffers[bufID] = &bufferEntry{
		buf:     buf,
		clients: map[uint64]struct{}{clientID: {}},
	}
	ver := buf.Version()
	s.mu.Unlock()

	res, err := call.AllocResults()
	if err != nil {
		return err
	}
	res.SetBufferId(bufID)
	if err := res.SetContent(content); err != nil {
		return err
	}
	res.SetVersion(ver)
	res.SetFromRecovery(fromRecovery)
	return nil
}

func (s *editorService) DiscardRecovery(_ context.Context, call proto.EditorService_discardRecovery) error {
	args := call.Args()
	bufID := args.BufferId()

	s.mu.Lock()
	entry, ok := s.buffers[bufID]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("unknown buffer %d", bufID)
	}
	path := entry.buf.Path()
	s.mu.Unlock()

	os.Remove(recoveryFilePath(s.recDir, path))

	content := ""
	if data, err := os.ReadFile(path); err == nil {
		content = string(data)
	}

	s.mu.Lock()
	if e, ok := s.buffers[bufID]; ok {
		e.buf = document.New(path, content)
	}
	s.mu.Unlock()

	res, err := call.AllocResults()
	if err != nil {
		return err
	}
	return res.SetContent(content)
}

// loadContent reads a file's content, preferring a newer recovery file if one exists.
func (s *editorService) loadContent(path string) (content string, fromRecovery bool) {
	var origModTime time.Time
	if info, err := os.Stat(path); err == nil {
		origModTime = info.ModTime()
	}
	if data, err := os.ReadFile(path); err == nil {
		content = string(data)
	}
	rp := recoveryFilePath(s.recDir, path)
	if recInfo, err := os.Stat(rp); err == nil && recInfo.ModTime().After(origModTime) {
		if recData, err := os.ReadFile(rp); err == nil {
			return string(recData), true
		}
	}
	return content, false
}

func (s *editorService) GetUpdates(_ context.Context, call proto.EditorService_getUpdates) error {
	args := call.Args()
	bufID := args.BufferId()
	since := args.SinceVersion()
	callerID := args.ClientId()

	s.mu.Lock()
	entry, ok := s.buffers[bufID]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("unknown buffer %d", bufID)
	}

	ops := entry.buf.OpsSince(since)
	// Filter out ops that originated from the caller.
	filtered := ops[:0:0]
	for _, op := range ops {
		if op.ClientID != callerID {
			filtered = append(filtered, op)
		}
	}

	res, err := call.AllocResults()
	if err != nil {
		return err
	}
	res.SetVersion(entry.buf.Version())

	if len(filtered) == 0 {
		return nil
	}

	list, err := res.NewOps(int32(len(filtered)))
	if err != nil {
		return err
	}
	for i, op := range filtered {
		item := list.At(i)
		item.SetClientId(op.ClientID)
		item.SetVersion(op.Version)
		switch op.Type {
		case document.OpInsert:
			item.SetType(proto.EditOp_OpType_insert)
			item.SetInsertLine(uint32(op.InsertLine))
			item.SetInsertCol(uint32(op.InsertCol))
			if err := item.SetInsertText(op.InsertText); err != nil {
				return err
			}
		case document.OpDelete:
			item.SetType(proto.EditOp_OpType_delete)
			item.SetFromLine(uint32(op.FromLine))
			item.SetFromCol(uint32(op.FromCol))
			item.SetToLine(uint32(op.ToLine))
			item.SetToCol(uint32(op.ToCol))
		}
	}
	return nil
}

func (s *editorService) ApplyOp(_ context.Context, call proto.EditorService_applyOp) error {
	args := call.Args()
	clientID := args.ClientId()
	bufID := args.BufferId()
	protoOp, err := args.Op()
	if err != nil {
		return err
	}

	s.mu.Lock()
	entry, ok := s.buffers[bufID]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("unknown buffer %d", bufID)
	}

	insertText, _ := protoOp.InsertText()
	op := document.Op{
		ClientID:   clientID,
		InsertLine: int(protoOp.InsertLine()),
		InsertCol:  int(protoOp.InsertCol()),
		InsertText: insertText,
		FromLine:   int(protoOp.FromLine()),
		FromCol:    int(protoOp.FromCol()),
		ToLine:     int(protoOp.ToLine()),
		ToCol:      int(protoOp.ToCol()),
	}
	switch protoOp.Type() {
	case proto.EditOp_OpType_insert:
		op.Type = document.OpInsert
	case proto.EditOp_OpType_delete:
		op.Type = document.OpDelete
	default:
		op.Type = document.OpNoop
	}

	newVersion := entry.buf.Apply(op)

	res, err := call.AllocResults()
	if err != nil {
		return err
	}
	res.SetVersion(newVersion)
	return nil
}

func (s *editorService) Save(_ context.Context, call proto.EditorService_save) error {
	args := call.Args()
	bufID := args.BufferId()

	s.mu.Lock()
	entry, ok := s.buffers[bufID]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("unknown buffer %d", bufID)
	}

	if err := os.WriteFile(entry.buf.Path(), []byte(entry.buf.Content()), 0644); err != nil {
		return err
	}
	entry.buf.SetClean()
	os.Remove(recoveryFilePath(s.recDir, entry.buf.Path()))

	_, err := call.AllocResults()
	return err
}

func (s *editorService) BufferClientCount(_ context.Context, call proto.EditorService_bufferClientCount) error {
	bufID := call.Args().BufferId()
	s.mu.Lock()
	entry, ok := s.buffers[bufID]
	var count uint32
	if ok {
		count = uint32(len(entry.clients))
	}
	s.mu.Unlock()
	res, err := call.AllocResults()
	if err != nil {
		return err
	}
	res.SetCount(count)
	return nil
}

func (s *editorService) CloseBuffer(_ context.Context, call proto.EditorService_closeBuffer) error {
	args := call.Args()
	clientID := args.ClientId()
	bufID := args.BufferId()

	var removedPath string
	s.mu.Lock()
	if entry, ok := s.buffers[bufID]; ok {
		delete(entry.clients, clientID)
		if len(entry.clients) == 0 {
			removedPath = entry.buf.Path()
			delete(s.buffers, bufID)
		}
	}
	s.mu.Unlock()

	if removedPath != "" {
		os.Remove(recoveryFilePath(s.recDir, removedPath))
	}

	_, err := call.AllocResults()
	return err
}

// DirtyBuffers returns paths of unsaved buffers.
func (s *editorService) DirtyBuffers() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, e := range s.buffers {
		if e.buf.Dirty() {
			out = append(out, e.buf.Path())
		}
	}
	return out
}

// Server wraps the listener and the RPC service.
type Server struct {
	socketPath string
	listener   net.Listener
	svc        *editorService
	done       chan struct{}
	connCount  atomic.Int64
}

// New creates and starts a server for the given working directory.
func New(dir string) (*Server, error) {
	sockPath := SocketPath(dir)
	os.Remove(sockPath) // clean up stale socket

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", sockPath, err)
	}
	if err := os.Chmod(sockPath, 0600); err != nil {
		ln.Close()
		os.Remove(sockPath)
		return nil, fmt.Errorf("securing socket %s: %w", sockPath, err)
	}

	recDir, err := setupRecoveryDir()
	if err != nil {
		ln.Close()
		os.Remove(sockPath)
		return nil, fmt.Errorf("recovery dir: %w", err)
	}

	srv := &Server{
		socketPath: sockPath,
		listener:   ln,
		done:       make(chan struct{}),
	}
	srv.svc = newEditorService(recDir, func() {
		close(srv.done)
	})

	cfg, _ := config.Load()
	interval := time.Duration(cfg.RecoveryIntervalSecs) * time.Second
	srv.startFlushLoop(interval, cfg.RecoveryMaxBytes)
	go srv.serve()
	return srv, nil
}

func (s *Server) startFlushLoop(interval time.Duration, maxBytes int64) {
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				s.flushDirtyBuffers(maxBytes)
			case <-s.done:
				return
			}
		}
	}()
}

func (s *Server) flushDirtyBuffers(maxBytes int64) {
	// Snapshot buffer references while holding the lock, then do all I/O outside it.
	s.svc.mu.Lock()
	bufs := make([]*document.Buffer, 0, len(s.svc.buffers))
	for _, e := range s.svc.buffers {
		bufs = append(bufs, e.buf)
	}
	s.svc.mu.Unlock()

	for _, buf := range bufs {
		rp := recoveryFilePath(s.svc.recDir, buf.Path())
		if !buf.Dirty() {
			os.Remove(rp)
			continue
		}
		if buf.ByteLen() > int(maxBytes) {
			os.Remove(rp)
			continue
		}
		content := buf.Content()
		if sha256.Sum256([]byte(content)) == buf.SavedHash() {
			// Content is back to saved state (e.g. after undo) — no recovery needed.
			os.Remove(rp)
		} else {
			os.WriteFile(rp, []byte(content), 0600)
		}
	}
}

func (s *Server) serve() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			select {
			case <-s.done:
			default:
			}
			return
		}
		s.connCount.Add(1)
		go func(c net.Conn) {
			defer func() {
				c.Close()
				s.connCount.Add(-1)
			}()
			transport := rpc.NewStreamTransport(c)
			opts := &rpc.Options{
				BootstrapClient: capnp.Client(proto.EditorService_ServerToClient(s.svc)),
			}
			conn := rpc.NewConn(transport, opts)
			defer conn.Close()
			select {
			case <-conn.Done():
			case <-s.done:
			}
		}(conn)
	}
}

// Wait blocks until the server should exit (all clients disconnected).
func (s *Server) Wait() {
	<-s.done
	s.listener.Close()
	s.deleteAllRecoveryFiles()
	os.Remove(s.socketPath)
}

func (s *Server) deleteAllRecoveryFiles() {
	s.svc.mu.Lock()
	paths := make([]string, 0, len(s.svc.buffers))
	for _, e := range s.svc.buffers {
		paths = append(paths, e.buf.Path())
	}
	s.svc.mu.Unlock()
	for _, p := range paths {
		os.Remove(recoveryFilePath(s.svc.recDir, p))
	}
}

// DirtyBuffers delegates to the service.
func (s *Server) DirtyBuffers() []string {
	return s.svc.DirtyBuffers()
}
