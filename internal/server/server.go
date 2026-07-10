package server

import (
	"context"
	"crypto/sha256"
	"errors"
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

func (s *editorService) OpenFile(_ context.Context, call proto.EditorService_openFile) error {
	args := call.Args()
	clientID := args.ClientId()
	path, err := args.Path()
	if err != nil {
		return err
	}
	serverLog("OpenFile: clientID=%d path=%q", clientID, path)

	content, fromRecovery := s.loadContent(path)

	s.mu.Lock()
	// Check if file is already open (skip dedup for untitled buffers).
	if path != "" {
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
	if path != "" {
		go s.lspMgr.DidOpen(path, content)
		go s.pluginMgr.DispatchBufferOpen(context.Background(), bufID, path)
		if s.watcher != nil {
			s.watcher.Add(path) //nolint:errcheck
		}
	}
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

	os.Remove(recoveryFilePath(s.recDir, path)) //nolint:errcheck

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
	if path == "" {
		return "", false // untitled buffer: no content, no recovery
	}
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

	path := entry.buf.Path()
	content := entry.buf.Content()
	go s.lspMgr.DidChange(path, content)
	go s.pluginMgr.DispatchBufferChange(context.Background(), bufID, path)

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

	path := entry.buf.Path()
	content := entry.buf.Content()

	if s.cfg.FormatOnSave {
		if formatted, changed, err := s.fmtMgr.Format(path, content); err == nil && changed {
			content = formatted
			entry.buf = document.New(path, content)
			s.mu.Lock()
			s.buffers[bufID] = entry
			s.mu.Unlock()
			go s.lspMgr.DidChange(path, content)
		}
	}

	s.markSaving(path)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		s.unmarkSaving(path)
		return err
	}
	s.unmarkSaving(path)
	entry.buf.SetClean()
	os.Remove(recoveryFilePath(s.recDir, path)) //nolint:errcheck
	go s.lspMgr.DidSave(path)
	go s.pluginMgr.DispatchBufferSave(context.Background(), bufID, path)

	_, err := call.AllocResults()
	return err
}

func (s *editorService) SaveAs(_ context.Context, call proto.EditorService_saveAs) error {
	args := call.Args()
	bufID := args.BufferId()
	newPath, err := args.Path()
	if err != nil {
		return err
	}

	s.mu.Lock()
	entry, ok := s.buffers[bufID]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("unknown buffer %d", bufID)
	}

	content := entry.buf.Content()
	s.markSaving(newPath)
	if err := os.WriteFile(newPath, []byte(content), 0o644); err != nil {
		s.unmarkSaving(newPath)
		return err
	}
	s.unmarkSaving(newPath)
	oldPath := entry.buf.Path()
	entry.buf = document.New(newPath, content)
	entry.buf.SetClean()
	s.mu.Lock()
	s.buffers[bufID] = entry
	s.mu.Unlock()

	if oldPath != "" {
		os.Remove(recoveryFilePath(s.recDir, oldPath)) //nolint:errcheck
	}
	go s.lspMgr.DidOpen(newPath, content)
	go s.pluginMgr.DispatchBufferOpen(context.Background(), bufID, newPath)

	_, err = call.AllocResults()
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
	serverLog("CloseBuffer: clientID=%d bufID=%d", clientID, bufID)

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

	// Always clean up the recovery file; for untitled buffers this removes the
	// sha256("") recovery entry so it isn't replayed on the next invocation.
	os.Remove(recoveryFilePath(s.recDir, removedPath)) //nolint:errcheck
	if removedPath != "" {
		if s.watcher != nil {
			s.watcher.Remove(removedPath) //nolint:errcheck
		}
		go s.lspMgr.DidClose(removedPath)
		go s.pluginMgr.DispatchBufferClose(context.Background(), bufID, removedPath)
	}

	_, err := call.AllocResults()
	return err
}

func (s *editorService) GetDiagnostics(_ context.Context, call proto.EditorService_getDiagnostics) error {
	bufID := call.Args().BufId()

	s.mu.Lock()
	entry, ok := s.buffers[bufID]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("unknown buffer %d", bufID)
	}
	path := entry.buf.Path()
	s.mu.Unlock()

	diags := s.lspMgr.GetDiagnostics(path)
	ready := s.lspMgr.HasClient(path)

	res, err := call.AllocResults()
	if err != nil {
		return err
	}
	res.SetLspReady(ready)
	if len(diags) == 0 {
		return nil
	}
	list, err := res.NewItems(int32(len(diags)))
	if err != nil {
		return err
	}
	for i, d := range diags {
		item := list.At(i)
		item.SetLine(uint32(d.Range.Start.Line))
		item.SetCol(uint32(d.Range.Start.Character))
		item.SetEndLine(uint32(d.Range.End.Line))
		item.SetEndCol(uint32(d.Range.End.Character))
		item.SetSeverity(uint8(d.Severity))
		item.SetMessage_(d.Message) //nolint:errcheck
		item.SetSource(d.Source)    //nolint:errcheck
	}
	return nil
}

func (s *editorService) Hover(_ context.Context, call proto.EditorService_hover) error {
	args := call.Args()
	bufID := args.BufId()
	line := int(args.Line())
	col := int(args.Col())

	s.mu.Lock()
	entry, ok := s.buffers[bufID]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("unknown buffer %d", bufID)
	}
	path := entry.buf.Path()
	s.mu.Unlock()

	res, err := call.AllocResults()
	if err != nil {
		return err
	}
	result, err := res.NewResult()
	if err != nil {
		return err
	}
	h, err := s.lspMgr.Hover(path, line, col)
	if err != nil {
		return err
	}
	if h == nil {
		return nil
	}
	text := h.Text()
	if text == "" {
		return nil
	}
	result.SetFound(true)
	result.SetContents(text) //nolint:errcheck
	return nil
}

func (s *editorService) SignatureHelp(_ context.Context, call proto.EditorService_signatureHelp) error {
	args := call.Args()
	bufID := args.BufId()
	line := int(args.Line())
	col := int(args.Col())

	s.mu.Lock()
	entry, ok := s.buffers[bufID]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("unknown buffer %d", bufID)
	}
	path := entry.buf.Path()
	s.mu.Unlock()

	res, err := call.AllocResults()
	if err != nil {
		return err
	}
	result, err := res.NewResult()
	if err != nil {
		return err
	}
	sh, err := s.lspMgr.SignatureHelp(path, line, col)
	if err != nil || sh == nil || len(sh.Signatures) == 0 {
		return nil
	}
	result.SetFound(true)
	result.SetActiveSignature(uint32(sh.ActiveSignature))
	result.SetActiveParameter(uint32(sh.ActiveParameter))
	sigs, err := result.NewSignatures(int32(len(sh.Signatures)))
	if err != nil {
		return nil
	}
	for i, sig := range sh.Signatures {
		s := sigs.At(i)
		s.SetLabel(sig.Label)                 //nolint:errcheck
		s.SetDocumentation(sig.Documentation) //nolint:errcheck
		if len(sig.Parameters) > 0 {
			params, err := s.NewParameters(int32(len(sig.Parameters)))
			if err != nil {
				continue
			}
			for j, p := range sig.Parameters {
				params.At(j).SetLabel(p.Label) //nolint:errcheck
			}
		}
	}
	return nil
}

func (s *editorService) Complete(_ context.Context, call proto.EditorService_complete) error {
	args := call.Args()
	bufID := args.BufId()
	line := int(args.Line())
	col := int(args.Col())

	s.mu.Lock()
	entry, ok := s.buffers[bufID]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("unknown buffer %d", bufID)
	}
	path := entry.buf.Path()
	s.mu.Unlock()

	items, err := s.lspMgr.Complete(path, line, col)
	res, rerr := call.AllocResults()
	if rerr != nil {
		return rerr
	}
	if err != nil || len(items) == 0 {
		return nil
	}
	list, err := res.NewItems(int32(len(items)))
	if err != nil {
		return err
	}
	for i, it := range items {
		ci := list.At(i)
		ci.SetLabel(it.Label) //nolint:errcheck
		ci.SetKind(uint8(it.Kind))
		ci.SetDetail(it.Detail)         //nolint:errcheck
		ci.SetInsertText(it.InsertText) //nolint:errcheck
	}
	return nil
}

func (s *editorService) Definition(_ context.Context, call proto.EditorService_definition) error {
	args := call.Args()
	bufID := args.BufId()
	line := int(args.Line())
	col := int(args.Col())

	s.mu.Lock()
	entry, ok := s.buffers[bufID]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("unknown buffer %d", bufID)
	}
	path := entry.buf.Path()
	s.mu.Unlock()

	locs, err := s.lspMgr.Definition(path, line, col)
	res, rerr := call.AllocResults()
	if rerr != nil {
		return rerr
	}
	if err != nil || len(locs) == 0 {
		return nil
	}
	loc := locs[0]
	result, err := res.NewResult()
	if err != nil {
		return err
	}
	result.SetFound(true)
	result.SetPath(lsp.URIToPath(loc.URI)) //nolint:errcheck
	result.SetLine(uint32(loc.Range.Start.Line))
	result.SetCol(uint32(loc.Range.Start.Character))
	return nil
}

func (s *editorService) HandlePluginKey(ctx context.Context, call proto.EditorService_handlePluginKey) error {
	args := call.Args()
	key, err := args.Key()
	if err != nil {
		return err
	}
	mode, err := args.Mode()
	if err != nil {
		return err
	}

	clientID := args.ClientId()
	bufID := args.BufId()
	cursorLine := args.CursorLine()
	cursorCol := args.CursorCol()
	handled, edits, cursorLine, cursorCol, hasCursor, captureKeys, handleErr := s.pluginMgr.HandleKey(ctx, key, mode, bufID, clientID, cursorLine, cursorCol)
	if handleErr != nil {
		return handleErr
	}

	// Apply edits returned by the plugin to the server buffer.
	// The plugin may have also called applyEdit directly; these cover the
	// case where the plugin returns edits atomically via KeyResponse.
	if handled && len(edits) > 0 {
		// edits reference a buffer by position but KeyResponse doesn't carry bufID;
		// we apply them via the bridge using the active buffer is ambiguous here.
		// For now, skip auto-application — plugins should call applyEdit explicitly.
		_ = edits
	}

	res, err := call.AllocResults()
	if err != nil {
		return err
	}
	result, err := res.NewResult()
	if err != nil {
		return err
	}
	result.SetHandled(handled)
	result.SetHasCursor(hasCursor)
	if hasCursor {
		result.SetCursorLine(cursorLine)
		result.SetCursorCol(cursorCol)
	}
	result.SetCaptureKeys(captureKeys)
	return nil
}

func (s *editorService) GetPluginDecorations(ctx context.Context, call proto.EditorService_getPluginDecorations) error {
	args := call.Args()
	clientID := args.ClientId()
	bufID := args.BufId()

	s.mu.Lock()
	ce, ok := s.clientMap[clientID]
	var topLine, height uint32
	if ok {
		topLine = ce.topLine
		height = ce.height
	}
	s.mu.Unlock()

	var endLine uint32
	if height > 0 {
		endLine = topLine + height - 1
	}
	decorations := s.pluginMgr.GetDecorations(ctx, clientID, bufID, topLine, endLine)

	res, err := call.AllocResults()
	if err != nil {
		return err
	}
	list, err := res.NewDecorations(int32(len(decorations)))
	if err != nil {
		return err
	}
	for i, d := range decorations {
		item := list.At(i)
		item.SetLine(d.Line)
		item.SetCol(d.Col)
		item.SetEndCol(d.EndCol)
		if err := item.SetText(d.Text); err != nil {
			return err
		}
		item.SetKind(proto.PluginDecorationKind(d.Kind))
		item.SetUnderlineStyle(proto.PluginUnderlineStyle(d.UnderlineStyle))
		if err := item.SetUnderlineColor(d.UnderlineColor); err != nil {
			return err
		}
		item.SetFixable(d.Fixable)
		if err := item.SetFixData(d.FixData); err != nil {
			return err
		}
		if err := item.SetPluginName(d.PluginName); err != nil {
			return err
		}
		if err := item.SetTextColor(d.TextColor); err != nil {
			return err
		}
	}
	return nil
}

func (s *editorService) GetPluginFixes(ctx context.Context, call proto.EditorService_getPluginFixes) error {
	args := call.Args()
	pluginName, err := args.PluginName()
	if err != nil {
		return err
	}
	fixData, err := args.FixData()
	if err != nil {
		return err
	}
	items, err := s.pluginMgr.GetFixes(ctx, pluginName, fixData)
	if err != nil {
		return err
	}
	res, resErr := call.AllocResults()
	if resErr != nil {
		return resErr
	}
	list, listErr := res.NewItems(int32(len(items)))
	if listErr != nil {
		return listErr
	}
	for i, it := range items {
		fi := list.At(i)
		if err := fi.SetLabel(it.Label); err != nil {
			return err
		}
		if err := fi.SetReplace(it.Replace); err != nil {
			return err
		}
	}
	return nil
}

func (s *editorService) ApplyPluginFix(ctx context.Context, call proto.EditorService_applyPluginFix) error {
	args := call.Args()
	pluginName, err := args.PluginName()
	if err != nil {
		return err
	}
	fixData, err := args.FixData()
	if err != nil {
		return err
	}
	index := args.Index()
	if err := s.pluginMgr.ApplyFix(ctx, pluginName, fixData, index); err != nil {
		return err
	}
	_, err = call.AllocResults()
	return err
}

func (s *editorService) GetPluginActions(ctx context.Context, call proto.EditorService_getPluginActions) error {
	args := call.Args()
	bufID := args.BufId()
	line := args.Line()
	col := args.Col()
	actions, err := s.pluginMgr.GetActionsAt(ctx, bufID, line, col)
	if err != nil {
		return err
	}
	res, resErr := call.AllocResults()
	if resErr != nil {
		return resErr
	}
	list, listErr := res.NewItems(int32(len(actions)))
	if listErr != nil {
		return listErr
	}
	for i, a := range actions {
		it := list.At(i)
		if err := it.SetLabel(a.Label); err != nil {
			return err
		}
		if err := it.SetReplace(a.Replace); err != nil {
			return err
		}
		it.SetFromLine(a.FromLine)
		it.SetFromCol(a.FromCol)
		it.SetToLine(a.ToLine)
		it.SetToCol(a.ToCol)
		if err := it.SetPluginName(a.PluginName); err != nil {
			return err
		}
	}
	return nil
}

func (s *editorService) ApplyPluginAction(ctx context.Context, call proto.EditorService_applyPluginAction) error {
	args := call.Args()
	pluginName, err := args.PluginName()
	if err != nil {
		return err
	}
	if err := s.pluginMgr.ApplyAction(ctx, pluginName, args.BufId(), args.Line(), args.Col(), args.Index()); err != nil {
		return err
	}
	_, err = call.AllocResults()
	return err
}

func (s *editorService) UpdateViewport(_ context.Context, call proto.EditorService_updateViewport) error {
	args := call.Args()
	clientID := args.ClientId()
	topLine := args.TopLine()
	height := args.Height()

	s.mu.Lock()
	if entry, ok := s.clientMap[clientID]; ok {
		entry.topLine = topLine
		entry.height = height
	}
	s.mu.Unlock()

	_, err := call.AllocResults()
	return err
}

func (s *editorService) GetPluginBindings(_ context.Context, call proto.EditorService_getPluginBindings) error {
	bindings := s.pluginMgr.AllPluginBindings()
	res, err := call.AllocResults()
	if err != nil {
		return err
	}
	list, err := res.NewBindings(int32(len(bindings)))
	if err != nil {
		return err
	}
	for i, b := range bindings {
		item := list.At(i)
		if err := item.SetPluginName(b.PluginName); err != nil {
			return err
		}
		if err := item.SetKey(b.Key); err != nil {
			return err
		}
		if err := item.SetDescription(b.Description); err != nil {
			return err
		}
	}
	return nil
}

func (s *editorService) GetPluginKeys(_ context.Context, call proto.EditorService_getPluginKeys) error {
	keys := s.pluginMgr.AllRegisteredKeys()
	serverLog("GetPluginKeys called, returning %d keys: %v", len(keys), keys)
	res, err := call.AllocResults()
	if err != nil {
		return err
	}
	list, err := res.NewKeys(int32(len(keys)))
	if err != nil {
		return err
	}
	for i, k := range keys {
		if err := list.Set(i, k); err != nil {
			return err
		}
	}
	return nil
}

func (s *editorService) Format(_ context.Context, call proto.EditorService_format) error {
	bufID := call.Args().BufId()

	s.mu.Lock()
	entry, ok := s.buffers[bufID]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("unknown buffer %d", bufID)
	}
	path := entry.buf.Path()
	content := entry.buf.Content()
	s.mu.Unlock()

	formatted, changed, fmtErr := s.fmtMgr.Format(path, content)

	noFormatter := errors.Is(fmtErr, format.ErrNoFormatter)
	if fmtErr != nil && !noFormatter {
		return fmtErr
	}

	if changed {
		newBuf := document.New(path, formatted)
		newBuf.MarkDirty()
		s.mu.Lock()
		entry.buf = newBuf
		s.buffers[bufID] = entry
		s.mu.Unlock()
		go s.lspMgr.DidChange(path, formatted)
	}

	res, err := call.AllocResults()
	if err != nil {
		return err
	}
	if changed {
		if err := res.SetContent(formatted); err != nil {
			return err
		}
	}
	res.SetChanged(changed)
	res.SetNoFormatter(noFormatter)
	return nil
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
	sockPath := SocketPath(dir)
	os.Remove(sockPath) //nolint:errcheck // clean up stale socket

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", sockPath, err)
	}
	if err := os.Chmod(sockPath, 0600); err != nil {
		ln.Close()          //nolint:errcheck
		os.Remove(sockPath) //nolint:errcheck
		return nil, fmt.Errorf("securing socket %s: %w", sockPath, err)
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
