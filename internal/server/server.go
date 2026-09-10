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

	"github.com/indiejames/indigo/internal/binstamp"
	"github.com/indiejames/indigo/internal/config"
	"github.com/indiejames/indigo/internal/document"
	"github.com/indiejames/indigo/internal/format"
	"github.com/indiejames/indigo/internal/lint"
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

// canonicalPath resolves path to a symlink-free absolute form, purely for
// deciding whether two path strings refer to the same underlying file — it
// is never used for display or storage; a buffer keeps whichever path
// spelling first opened it (see bufferEntry.canonPath).
//
// This matters because nothing upstream of the server guarantees path
// strings are already equivalent-comparable: a file opened once through the
// workspace's literal directory structure (file picker, grep results, the
// initial CLI argument) and once through a language server's report of the
// "same" file (go-to-definition, references, rename) can arrive as two
// different strings for the identical file on disk. TypeScript's tsserver
// in particular resolves symlinks by default when reporting locations
// (its preserveSymlinks option defaults to false), which bites hardest in
// exactly the projects most likely to have symlinked node_modules or
// workspace packages — pnpm and monorepo setups. Without this, the two
// spellings silently created two independent bufferEntry values for one
// file, with independent (and divergent) dirty state.
//
// Returns path unchanged if it can't be resolved (e.g. the file doesn't
// exist yet, as for a brand-new buffer that hasn't been saved).
func canonicalPath(path string) string {
	if path == "" {
		return path
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return path
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
	// canonPath is buf.Path() resolved via canonicalPath, computed once when
	// the entry is created (or renamed via SaveAs) — see canonicalPath's doc
	// comment for why this exists.
	canonPath string
	// generation increments every time buf is replaced with a new
	// *document.Buffer object (format-on-save, SaveAs, DiscardRecovery,
	// explicit Format) rather than edited via buf.Apply. See the
	// generation doc comment on openFile/getUpdates in editor.capnp.
	generation uint64
	// sinceByClient records, per connected clientID, the last buffer
	// version that client is known to have caught up to (via GetUpdates or
	// its own ApplyOp/ApplyOps). Used by recordClientProgress to compute a
	// safe watermark for document.Buffer.TrimHistory — see that function's
	// doc comment.
	sinceByClient map[uint64]uint64
	// pluginDiags holds each plugin's most recently published diagnostics
	// for this buffer, keyed by plugin name (see PluginPublishDiagnostics),
	// alongside the buffer version they were computed against. GetDiagnostics
	// excludes an entry once buf's version moves past that — the publish-time
	// check in PluginPublishDiagnostics only stops an old publish from
	// overwriting a newer one; it does nothing once accepted diagnostics are
	// left behind by further edits and start pointing at the wrong text.
	// Lives on bufferEntry rather than a separate map so it's cleaned up
	// automatically when the entry itself is removed in CloseBuffer/Save's
	// wholesale-swap paths — no separate forget-on-close call needed.
	pluginDiags map[string]pluginDiagEntry
}

// pluginDiagEntry is one plugin's cached diagnostics for a buffer, tagged
// with the buffer version they were computed against — see pluginDiags.
type pluginDiagEntry struct {
	version uint64
	diags   []lsp.Diagnostic
}

// clientEntry holds connection metadata for a connected client.
type clientEntry struct {
	callback proto.ClientCallback
	topLine  uint32
	height   uint32
	// connID is the connection this client registered on, recorded so the
	// connection dying can undo the registration. Disconnect is an explicit
	// RPC and a crashed, killed, or timed-out client never sends it; without
	// this there is nothing linking the corpse to the buffers it holds open.
	// Zero for a client registered outside a real connection (tests calling
	// Connect on *editorService directly), which never matches a live connID.
	connID uint64
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
	lintMgr   *lint.Manager
	pluginMgr *plugin.Manager
	cfg       *config.Config

	watcher     *fsnotify.Watcher
	watchMu     sync.Mutex
	dirWatches  map[string]int // containing dir -> number of watched paths inside it
	savingMu    sync.Mutex
	savingPaths map[string]time.Time // paths currently being saved by indigo

	// Plugin-driven popup state. Stored while a popup is visible on clients.
	popupOnSelect func(data string)
	popupOnCancel func()
	popupItems    []plugin.PluginPopupItem

	// Plugin-driven input prompt state.
	inputOnConfirm func(text string)
	inputOnCancel  func()

	// popupDispatchMu serializes PluginShowPopup/PluginShowInputPrompt's
	// client notification dispatch: each call's fan-out to every client
	// runs under this lock (clients notified in parallel within one call,
	// but one call's dispatch fully finishes before the next call's
	// begins), so a later call's UI push can never reach a client before
	// an earlier call's and leave it showing stale popup/prompt contents
	// bound to an already-replaced server-side callback.
	popupDispatchMu sync.Mutex

	// activeCtx tracks the most recently active client buffer for external tools.
	activeCtxMu sync.RWMutex
	activeCtx   activeContext
	activeSel   activeSelection

	// pluginWorkspaceDiags holds diagnostics published via
	// publishWorkspaceDiagnostics, keyed by file path then plugin name — the
	// path-keyed sibling of bufferEntry.pluginDiags, for files that aren't
	// open in any buffer (see PluginPublishWorkspaceDiagnostics). A later
	// publish for the same (path, plugin) replaces the previous one; an
	// empty diagnostics list clears the entry. Merged into
	// allWorkspaceDiagnostics for paths not covered by an open buffer.
	pluginWorkspaceDiags map[string]map[string][]lsp.Diagnostic

	// statusBar holds client-contributed status bar text segments.
	statusBar *statusBarRegistry

	// staleWatch remembers the binaries this process started from, so Connect
	// can tell a client it is talking to code that has since been replaced on
	// disk. See staleness.go.
	staleWatch *staleWatch

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
			Address:    ls.Address,
		}
	}
	lspMgr := lsp.NewManager(workspaceDir, servers)
	watcher, _ := fsnotify.NewWatcher()
	svc := &editorService{
		staleWatch:           newStaleWatch(),
		buffers:              make(map[uint32]*bufferEntry),
		clientMap:            make(map[uint64]*clientEntry),
		recDir:               recDir,
		lspMgr:               lspMgr,
		watcher:              watcher,
		dirWatches:           make(map[string]int),
		savingPaths:          make(map[string]time.Time),
		fmtMgr:               format.NewManager(lspMgr, cfg, workspaceDir),
		lintMgr:              lint.NewManager(cfg, workspaceDir),
		cfg:                  cfg,
		shutdown:             shutdown,
		onClientConnect:      onClientConnect,
		statusBar:            newStatusBarRegistry(),
		pluginWorkspaceDiags: make(map[string]map[string][]lsp.Diagnostic),
	}
	svc.pluginMgr = plugin.NewManager(workspaceDir, svc)
	if watcher != nil {
		go svc.watchLoop()
	}
	// Kick off an initial workspace lint scan so files nobody has opened
	// yet already have something in getWorkspaceDiagnostics/Summary by the
	// time a client first asks — see lint.Manager.ScanWorkspace's doc
	// comment for why this only runs at startup and on explicit rescan, not
	// per-edit. ScanWorkspace itself only ever launches its own background
	// goroutine and returns immediately, so no extra `go` is needed here.
	svc.lintMgr.ScanWorkspace()
	// Also kick off an LSP workspace-diagnostic scan at the same trigger
	// point, for parity with the lint scan above — see
	// lsp.Manager.ScanWorkspace's doc comment. In practice this is a no-op
	// here: no language server has started yet, since nothing has been
	// opened this session. It becomes productive once a rescan
	// (RescanWorkspaceDiagnostics) runs after at least one LSP-backed file
	// has been opened and its server initialized.
	svc.lspMgr.ScanWorkspace()
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

	canonPath := canonicalPath(path)
	s.mu.Lock()
	var bufID uint32
	var entry *bufferEntry
	for id, e := range s.buffers {
		if e.canonPath == canonPath {
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

// addPathWatch registers path for external-change notifications by watching
// its containing directory rather than the file itself. kqueue-style watches
// (used by fsnotify on macOS) are tied to a specific inode: when a file is
// replaced — as `git checkout` does, via unlink+create rather than an
// in-place write — the watch on that inode is stranded, and fsnotify's own
// best-effort recovery for this case is racy and can silently drop the watch
// for good. Watching the containing directory sidesteps the problem entirely
// since the directory's inode isn't replaced when a file inside it is.
func (s *editorService) addPathWatch(path string) {
	if s.watcher == nil || path == "" {
		return
	}
	dir := filepath.Dir(path)
	s.watchMu.Lock()
	defer s.watchMu.Unlock()
	if s.dirWatches[dir] == 0 {
		s.watcher.Add(dir) //nolint:errcheck
	}
	s.dirWatches[dir]++
}

// removePathWatch undoes a prior addPathWatch, dropping the directory watch
// once no watched path inside it remains.
func (s *editorService) removePathWatch(path string) {
	if s.watcher == nil || path == "" {
		return
	}
	dir := filepath.Dir(path)
	s.watchMu.Lock()
	defer s.watchMu.Unlock()
	if s.dirWatches[dir] == 0 {
		return
	}
	s.dirWatches[dir]--
	if s.dirWatches[dir] == 0 {
		delete(s.dirWatches, dir)
		s.watcher.Remove(dir) //nolint:errcheck
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

// Connect on *editorService satisfies the interface but is unreachable in
// practice because all real connections go through connSvc. It falls back to
// connID 0 (no auto-cleanup when the connection dies), matching
// SetStatusBarText's split for the same reason.
func (s *editorService) Connect(ctx context.Context, call proto.EditorService_connect) error {
	return s.connect(0, call)
}

// Connect records the connection the client arrived on, so dropConnection can
// release everything it holds if it goes away without calling Disconnect.
func (c *connSvc) Connect(_ context.Context, call proto.EditorService_connect) error {
	return c.connect(c.connID, call)
}

func (s *editorService) connect(connID uint64, call proto.EditorService_connect) error {
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
	s.clientMap[id] = &clientEntry{callback: cbOwned, connID: connID}
	s.mu.Unlock()
	serverLog("Connect: stored clientID=%d, callback.IsValid=%v", id, cbOwned.IsValid())
	res.SetClientId(id)
	// Plugin binaries are registered lazily: plugins start asynchronously
	// after the server does, so they cannot all be stamped at construction.
	// The stamp comes from the plugin manager rather than being taken here,
	// because "here" can be hours after the plugin launched — restatting the
	// file now would adopt a build installed since as the baseline and report
	// a genuinely stale plugin as current.
	for _, bin := range s.pluginMgr.BinaryStamps() {
		s.staleWatch.watchStamped(bin.Path, binstamp.Stamp{
			Size:    bin.Size,
			ModTime: bin.ModTimeUnixNano,
		})
	}
	if desc := s.staleWatch.staleDescription(); desc != "" {
		// Deliberately reported, not acted on: someone may be editing in this
		// server, and dropping their session to pick up a new build would be
		// worse than serving old code for a while longer. The client decides
		// how loudly to say it.
		serverLog("Connect: serving a stale build — %s", desc)
		res.SetServerStale(true)
	}
	if s.onClientConnect != nil {
		s.onClientConnect()
	}
	return nil
}

func (s *editorService) Disconnect(_ context.Context, call proto.EditorService_disconnect) error {
	clientID := call.Args().ClientId()
	serverLog("Disconnect called for clientID=%d", clientID)
	if remaining := s.dropClients([]uint64{clientID}); remaining == 0 {
		s.shutdown()
	}
	_, err := call.AllocResults()
	return err
}

// dropConnection releases every client that registered on connID, for a
// connection that has gone away.
//
// Disconnect is an explicit RPC, so it is sent only by a client that quits in
// an orderly way. A crashed window, a `kill`, a dropped ssh session, or an
// agent tool whose context expired mid-sequence all end the connection without
// it — and until this existed, the corpse stayed registered as a client of
// every buffer it had open, forever. That kept those buffers pinned: CloseBuffer
// only frees a buffer once its client set empties, so the entry lived on with
// whatever content it had, and OpenFile's attach path then served that content
// to the next window to open the file. The file could be changed on disk in the
// meantime and nothing would ever re-read it — not the watcher (whose reload is
// CloseBuffer + OpenFile, which re-attached to the same pinned entry), and not
// closing and reopening the file by hand. Only restarting the server recovered.
//
// It also pinned buffer history: recordClientProgress takes the minimum
// version across registered clients, so a dead client's watermark blocked
// TrimHistory for the life of the process.
func (s *editorService) dropConnection(connID uint64) {
	if connID == 0 {
		return
	}
	s.mu.Lock()
	var ids []uint64
	for id, e := range s.clientMap {
		if e.connID == connID {
			ids = append(ids, id)
		}
	}
	s.mu.Unlock()
	if len(ids) == 0 {
		return // the normal case: the client called Disconnect before hanging up
	}
	serverLog("dropConnection: connID=%d ended without Disconnect, releasing client(s) %v", connID, ids)
	s.dropClients(ids)
}

// dropClients unregisters the given clients and frees any buffer left with no
// clients at all, returning how many clients remain connected.
//
// Freeing the buffer is the point: a clientless buffer can never be read,
// edited, or saved by anyone, but for as long as it sits in s.buffers it keeps
// answering OpenFile with its in-memory content instead of the file.
func (s *editorService) dropClients(ids []uint64) (remaining int) {
	// What each freed buffer needs after the lock is released. Captured under
	// the lock because the entry is gone by then.
	type orphan struct {
		bufID   uint32
		path    string
		content string
		dirty   bool
		tooBig  bool
	}
	var orphans []orphan

	s.mu.Lock()
	for _, id := range ids {
		if entry, ok := s.clientMap[id]; ok {
			entry.callback.Release()
			delete(s.clientMap, id)
		}
		for bufID, e := range s.buffers {
			if _, held := e.clients[id]; !held {
				continue
			}
			delete(e.clients, id)
			delete(e.sinceByClient, id)
			if len(e.clients) > 0 {
				continue
			}
			o := orphan{bufID: bufID, path: e.buf.Path(), dirty: e.buf.Dirty()}
			if o.dirty {
				o.content = e.buf.Content()
				o.tooBig = int64(e.buf.ByteLen()) > s.cfg.RecoveryMaxBytes
			}
			orphans = append(orphans, o)
			delete(s.buffers, bufID)
		}
	}
	remaining = len(s.clientMap)
	s.mu.Unlock()

	for _, o := range orphans {
		serverLog("dropClients: freeing buffer %d (%q), last client gone (dirty=%v)", o.bufID, o.path, o.dirty)
		if o.path == "" {
			continue // untitled: nothing watched, nothing to recover to, no LSP
		}
		// Unlike CloseBuffer — an explicit "I'm done with this file" — losing a
		// client is not consent to discard unsaved work, so a dirty buffer's
		// recovery file is written from its final content rather than removed.
		// Writing it here rather than leaving it to the next flushDirtyBuffers
		// tick matters because the buffer is being freed right now: whatever the
		// last periodic flush missed would otherwise be gone.
		rp := recoveryFilePath(s.recDir, o.path)
		switch {
		case o.dirty && !o.tooBig:
			os.WriteFile(rp, []byte(o.content), 0600) //nolint:errcheck
		default:
			os.Remove(rp) //nolint:errcheck
		}
		s.removePathWatch(o.path)
		go s.lspMgr.DidClose(o.path)
		s.lintMgr.Forget(o.path)
		go s.pluginMgr.DispatchBufferClose(context.Background(), o.bufID, o.path)
	}
	return remaining
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
	nextConnID   atomic.Uint64
	hasHadClient atomic.Bool
	shutdownOnce sync.Once
}

// triggerShutdown closes the done channel exactly once, causing Wait() to unblock.
func (s *Server) triggerShutdown() {
	s.shutdownOnce.Do(func() {
		serverLog("triggerShutdown: closing done channel")
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

	cfg, err := config.Load()
	if err != nil {
		ln.Close()          //nolint:errcheck
		os.Remove(sockPath) //nolint:errcheck
		return nil, fmt.Errorf("load config: %w", err)
	}

	srv := &Server{
		socketPath: sockPath,
		listener:   ln,
		done:       make(chan struct{}),
	}
	srv.svc = newEditorService(recDir, dir, cfg, srv.triggerShutdown, func() {
		srv.hasHadClient.Store(true)
	})

	go func() {
		srv.svc.pluginMgr.Start(context.Background()) //nolint:errcheck
		// Kick off an initial workspace scan for any plugin that registered a
		// scan handler (e.g. indigo-spell), mirroring the lint workspace scan
		// kicked off just above in newEditorService — Start already blocked
		// until every discovered plugin finished (or failed) initializing,
		// so registrations from initialize are guaranteed visible here.
		srv.svc.pluginMgr.DispatchWorkspaceScan(context.Background())
	}()

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
		connID := s.nextConnID.Add(1)
		go func(c net.Conn, connID uint64) {
			defer func() {
				if r := recover(); r != nil {
					buf := make([]byte, 64*1024)
					n := runtime.Stack(buf, true)
					serverLog("serve: PANIC: %v\n%s", r, buf[:n])
				}
				s.svc.statusBar.clearForConn(connID)
				// Same reasoning as the status-bar cleanup above, for the
				// registrations that outlive the connection far more
				// damagingly — see dropConnection.
				s.svc.dropConnection(connID)
				newCount := s.connCount.Add(-1)
				serverLog("serve: connection closed, connCount now %d, hasHadClient=%v", newCount, s.hasHadClient.Load())
				c.Close() //nolint:errcheck
				if newCount == 0 && s.hasHadClient.Load() {
					s.triggerShutdown()
				}
			}()
			transport := rpc.NewStreamTransport(c)
			svc := &connSvc{editorService: s.svc, connID: connID}
			opts := &rpc.Options{
				BootstrapClient: capnp.Client(proto.EditorService_ServerToClient(svc)),
				Logger:          &serverRPCLogger{},
			}
			conn := rpc.NewConn(transport, opts)
			defer conn.Close() //nolint:errcheck
			select {
			case <-conn.Done():
				serverLog("serve: connection dropped by peer")
			case <-s.done:
				serverLog("serve: s.done fired")
			}
		}(conn, connID)
	}
}

// Wait blocks until the server should exit (all clients disconnected).
func (s *Server) Wait() {
	<-s.done
	s.listener.Close() //nolint:errcheck
	s.deleteAllRecoveryFiles()
	s.svc.lspMgr.Shutdown()
	s.svc.pluginMgr.Shutdown()
	s.svc.lintMgr.Shutdown()
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
