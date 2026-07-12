package server

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	capnp "capnproto.org/go/capnp/v3"
	"capnproto.org/go/capnp/v3/rpc"
	"github.com/fsnotify/fsnotify"

	"github.com/indiejames/indigo/internal/config"
	"github.com/indiejames/indigo/internal/document"
	"github.com/indiejames/indigo/internal/format"
	"github.com/indiejames/indigo/internal/lsp"
	"github.com/indiejames/indigo/internal/plugin"
	proto "github.com/indiejames/indigo/internal/proto"
)

// serverRPCLogger implements rpc.Logger, routing capnproto internal messages to the log file.
type serverRPCLogger struct{}

func (l *serverRPCLogger) Debug(msg string, args ...any) { serverLog("rpc debug: %s %v", msg, args) }
func (l *serverRPCLogger) Info(msg string, args ...any)  { serverLog("rpc info: %s %v", msg, args) }
func (l *serverRPCLogger) Warn(msg string, args ...any)  { serverLog("rpc warn: %s %v", msg, args) }
func (l *serverRPCLogger) Error(msg string, args ...any) { serverLog("rpc error: %s %v", msg, args) }

// socketDir returns the private 0700 directory that holds sockets for a workspace.
// Including the UID ensures different users on the same machine never share a directory.
func socketDir(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = dir
	}
	h := sha256.Sum256([]byte(abs))
	return filepath.Join(os.TempDir(), fmt.Sprintf("indigo-%d-%x", os.Getuid(), h[:8]))
}

// SocketPath returns the Unix socket path for a given working directory.
// The socket lives inside a per-user 0700 directory so no time-of-check /
// time-of-use race is possible between creating and chmod-ing the socket.
func SocketPath(dir string) string {
	return filepath.Join(socketDir(dir), "server.sock")
}

// IsRunning returns true if a server socket exists and is accepting connections.
func IsRunning(socketPath string) bool {
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return false
	}
	conn.Close() //nolint:errcheck
	return true
}

// bufferEntry holds a buffer and tracks which clients have it open.
type bufferEntry struct {
	buf     *document.Buffer
	clients map[uint64]struct{}
}

// clientEntry holds connection metadata for a connected client.
type clientEntry struct {
	callback proto.ClientCallback
	topLine  uint32
	height   uint32
}

// editorService implements proto.EditorService_Server.
type editorService struct {
	mu        sync.Mutex
	buffers   map[uint32]*bufferEntry
	nextBuf   uint32
	clientMap map[uint64]*clientEntry
	nextClt   uint64
	recDir    string
	lspMgr    *lsp.Manager
	fmtMgr    *format.Manager
	pluginMgr *plugin.Manager
	cfg       *config.Config

	watcher      *fsnotify.Watcher
	savingMu     sync.Mutex
	savingPaths  map[string]time.Time // paths currently being saved by indigo

	// Plugin-driven popup state. Stored while a popup is visible on clients.
	popupOnSelect func(data string)
	popupOnCancel func()
	popupItems    []plugin.PluginPopupItem

	// Plugin-driven input prompt state.
	inputOnConfirm func(text string)
	inputOnCancel  func()

	// shutdown is called when the last client disconnects (cleanly or not).
	shutdown        func()
	onClientConnect func() // called once per Connect RPC; used to mark that real clients have connected
}

func newEditorService(recDir, workspaceDir string, cfg *config.Config, shutdown func(), onClientConnect func()) *editorService {
	servers := make([]lsp.ServerConfig, len(cfg.EffectiveLanguageServers()))
	for i, ls := range cfg.EffectiveLanguageServers() {
		servers[i] = lsp.ServerConfig{
			Extensions: ls.Extensions,
			Command:    ls.Command,
			Args:       ls.Args,
		}
	}
	lspMgr := lsp.NewManager(workspaceDir, servers)
	watcher, _ := fsnotify.NewWatcher()
	svc := &editorService{
		buffers:         make(map[uint32]*bufferEntry),
		clientMap:       make(map[uint64]*clientEntry),
		recDir:          recDir,
		lspMgr:          lspMgr,
		watcher:         watcher,
		savingPaths:     make(map[string]time.Time),
		fmtMgr:          format.NewManager(lspMgr, cfg, workspaceDir),
		cfg:             cfg,
		shutdown:        shutdown,
		onClientConnect: onClientConnect,
	}
	svc.pluginMgr = plugin.NewManager(workspaceDir, svc)
	if watcher != nil {
		go svc.watchLoop()
	}
	return svc
}

// watchLoop processes fsnotify events and notifies clients when a file they
// have open is modified externally.
func (s *editorService) watchLoop() {
	for {
		select {
		case event, ok := <-s.watcher.Events:
			if !ok {
				return
			}
			serverLog("watchLoop: event=%s name=%q", event.Op, event.Name)
			if event.Has(fsnotify.Write) || event.Has(fsnotify.Create) || event.Has(fsnotify.Rename) {
				s.handleExternalWrite(event.Name)
			}
		case err, ok := <-s.watcher.Errors:
			if !ok {
				return
			}
			serverLog("watchLoop: error=%v", err)
		}
	}
}

// handleExternalWrite is called when fsnotify reports a write to a watched file.
func (s *editorService) handleExternalWrite(path string) {
	serverLog("handleExternalWrite: path=%q", path)

	// Ignore writes we caused ourselves (Save/SaveAs).
	s.savingMu.Lock()
	savedAt, isSaving := s.savingPaths[path]
	s.savingMu.Unlock()
	if isSaving && time.Since(savedAt) < 2*time.Second {
		serverLog("handleExternalWrite: skipping (self-save at %v)", savedAt)
		return
	}

	s.mu.Lock()
	var bufID uint32
	var entry *bufferEntry
	for id, e := range s.buffers {
		if e.buf.Path() == path {
			bufID = id
			entry = e
			break
		}
	}
	if entry == nil {
		serverLog("handleExternalWrite: no buffer for path %q (buffers: %d)", path, len(s.buffers))
		s.mu.Unlock()
		return
	}
	dirty := entry.buf.Dirty()
	callbacks := s.callbacksForBuffer(entry)
	s.mu.Unlock()

	serverLog("handleExternalWrite: notifying %d clients for bufID=%d dirty=%v", len(callbacks), bufID, dirty)
	ctx := context.Background()
	for i, cb := range callbacks {
		fut, rel := cb.FileChanged(ctx, func(p proto.ClientCallback_fileChanged_Params) error {
			p.SetBufId(bufID)
			p.SetDirty(dirty)
			return nil
		})
		_, err := fut.Struct()
		rel()
		serverLog("handleExternalWrite: client[%d] FileChanged returned err=%v", i, err)
	}
}

// markSaving records that we are about to write path ourselves so the watcher
// can ignore the resulting event.
func (s *editorService) markSaving(path string) {
	s.savingMu.Lock()
	s.savingPaths[path] = time.Now()
	s.savingMu.Unlock()
}

// unmarkSaving clears the saving flag for path after a short delay to absorb
// any late fsnotify events that arrive after the write completes.
func (s *editorService) unmarkSaving(path string) {
	go func() {
		time.Sleep(500 * time.Millisecond)
		s.savingMu.Lock()
		delete(s.savingPaths, path)
		s.savingMu.Unlock()
	}()
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
	cb := call.Args().Callback()
	res, err := call.AllocResults()
	if err != nil {
		cb.Release()
		return err
	}
	// AddRef the callback so our stored reference survives after the call
	// finalizes and releases the capability it received from the call args.
	cbOwned := proto.ClientCallback(capnp.Client(cb).AddRef())
	s.mu.Lock()
	s.nextClt++
	id := s.nextClt
	s.clientMap[id] = &clientEntry{callback: cbOwned}
	s.mu.Unlock()
	serverLog("Connect: stored clientID=%d, callback.IsValid=%v", id, cbOwned.IsValid())
	res.SetClientId(id)
	if s.onClientConnect != nil {
		s.onClientConnect()
	}
	return nil
}

func (s *editorService) Disconnect(_ context.Context, call proto.EditorService_disconnect) error {
	clientID := call.Args().ClientId()
	serverLog("Disconnect called for clientID=%d", clientID)
	s.mu.Lock()
	if entry, ok := s.clientMap[clientID]; ok {
		entry.callback.Release()
		delete(s.clientMap, clientID)
	}
	// Remove client from any open buffers.
	for _, e := range s.buffers {
		delete(e.clients, clientID)
	}
	remaining := len(s.clientMap)
	s.mu.Unlock()

	if remaining == 0 {
		s.shutdown()
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
	socketPath   string
	listener     net.Listener
	svc          *editorService
	done         chan struct{}
	connCount    atomic.Int64
	hasHadClient atomic.Bool
	shutdownOnce sync.Once
}

// triggerShutdown closes the done channel exactly once, causing Wait() to unblock.
func (s *Server) triggerShutdown() {
	s.shutdownOnce.Do(func() {
		buf := make([]byte, 16*1024)
		n := runtime.Stack(buf, false)
		serverLog("triggerShutdown: closing done channel, caller stack:\n%s", buf[:n])
		close(s.done)
	})
}

// New creates and starts a server for the given working directory.
func New(dir string) (*Server, error) {
	// Create (or tighten) the private socket directory before creating the socket
	// so it is never world-accessible, even for the instant between Listen and
	// a subsequent chmod call.
	sockDir := socketDir(dir)
	if err := os.MkdirAll(sockDir, 0o700); err != nil {
		return nil, fmt.Errorf("create socket dir %s: %w", sockDir, err)
	}
	// MkdirAll does not change permissions of existing dirs; enforce 0700 explicitly.
	if err := os.Chmod(sockDir, 0o700); err != nil {
		return nil, fmt.Errorf("secure socket dir %s: %w", sockDir, err)
	}

	sockPath := SocketPath(dir)
	os.Remove(sockPath) //nolint:errcheck // clean up stale socket

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", sockPath, err)
	}

	recDir, err := setupRecoveryDir()
	if err != nil {
		ln.Close()          //nolint:errcheck
		os.Remove(sockPath) //nolint:errcheck
		return nil, fmt.Errorf("recovery dir: %w", err)
	}

	cfg, _ := config.Load()

	srv := &Server{
		socketPath: sockPath,
		listener:   ln,
		done:       make(chan struct{}),
	}
	srv.svc = newEditorService(recDir, dir, cfg, srv.triggerShutdown, func() {
		srv.hasHadClient.Store(true)
	})

	go srv.svc.pluginMgr.Start(context.Background()) //nolint:errcheck

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
		if buf.Path() == "" {
			continue // untitled buffers have no file to recover to
		}
		rp := recoveryFilePath(s.svc.recDir, buf.Path())
		if !buf.Dirty() {
			os.Remove(rp) //nolint:errcheck
			continue
		}
		if buf.ByteLen() > int(maxBytes) {
			os.Remove(rp) //nolint:errcheck
			continue
		}
		content := buf.Content()
		if sha256.Sum256([]byte(content)) == buf.SavedHash() {
			// Content is back to saved state (e.g. after undo) — no recovery needed.
			os.Remove(rp) //nolint:errcheck
		} else {
			os.WriteFile(rp, []byte(content), 0600) //nolint:errcheck
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
				if r := recover(); r != nil {
					buf := make([]byte, 64*1024)
					n := runtime.Stack(buf, true)
					serverLog("serve: PANIC: %v\n%s", r, buf[:n])
				}
				newCount := s.connCount.Add(-1)
				serverLog("serve: connection closed, connCount now %d, hasHadClient=%v", newCount, s.hasHadClient.Load())
				c.Close() //nolint:errcheck
				if newCount == 0 && s.hasHadClient.Load() {
					s.triggerShutdown()
				}
			}()
			transport := rpc.NewStreamTransport(c)
			opts := &rpc.Options{
				BootstrapClient: capnp.Client(proto.EditorService_ServerToClient(s.svc)),
				Logger:          &serverRPCLogger{},
			}
			conn := rpc.NewConn(transport, opts)
			defer conn.Close() //nolint:errcheck
			select {
			case <-conn.Done():
				buf := make([]byte, 64*1024)
				n := runtime.Stack(buf, true)
				serverLog("serve: conn.Done() fired! goroutines:\n%s", buf[:n])
			case <-s.done:
				serverLog("serve: s.done fired")
			}
		}(conn)
	}
}

// Wait blocks until the server should exit (all clients disconnected).
func (s *Server) Wait() {
	<-s.done
	s.listener.Close() //nolint:errcheck
	s.deleteAllRecoveryFiles()
	s.svc.lspMgr.Shutdown()
	s.svc.pluginMgr.Shutdown()
	if s.svc.watcher != nil {
		s.svc.watcher.Close() //nolint:errcheck
	}
	os.Remove(s.socketPath) //nolint:errcheck
}

func (s *Server) deleteAllRecoveryFiles() {
	s.svc.mu.Lock()
	paths := make([]string, 0, len(s.svc.buffers))
	for _, e := range s.svc.buffers {
		paths = append(paths, e.buf.Path())
	}
	s.svc.mu.Unlock()
	for _, p := range paths {
		os.Remove(recoveryFilePath(s.svc.recDir, p)) //nolint:errcheck
	}
}

// DirtyBuffers delegates to the service.
func (s *Server) DirtyBuffers() []string {
	return s.svc.DirtyBuffers()
}
