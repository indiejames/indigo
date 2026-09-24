package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/indiejames/indigo/internal/document"
	"github.com/indiejames/indigo/internal/faultinject"
	proto "github.com/indiejames/indigo/internal/proto"
	"github.com/indiejames/indigo/internal/syncevent"
)

// atomicWriteFile writes data to path via a temp file in the same directory
// followed by a rename, so a crash mid-write can never leave a truncated
// file. The existing file's permissions are preserved when present.
func atomicWriteFile(path string, data []byte, defaultMode os.FileMode) error {
	mode := defaultMode
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".indigo-save-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) //nolint:errcheck // no-op after successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close() //nolint:errcheck
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close() //nolint:errcheck
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func (s *editorService) OpenFile(_ context.Context, call proto.EditorService_openFile) error {
	args := call.Args()
	clientID := args.ClientId()
	path, err := args.Path()
	if err != nil {
		return err
	}
	serverLog("OpenFile: clientID=%d path=%q", clientID, path)

	// Attach to an already-open buffer before touching the filesystem: that
	// path serves the in-memory content and never uses what's on disk, so a
	// file that has since become unreadable must not stop a second client
	// (another window, a plugin's RequestOpenFile, a reload) from joining a
	// buffer that is open and perfectly usable.
	s.mu.Lock()
	if bufID, content, ver, gen, ok := s.attachToOpenBufferLocked(path, clientID); ok {
		s.mu.Unlock()
		return setOpenFileResults(call, bufID, content, ver, gen, false)
	}
	s.mu.Unlock()

	// Read outside the lock — a slow or hung filesystem must not block every
	// other RPC on this connection.
	content, fromRecovery, crlf, err := s.loadContent(path)
	if err != nil {
		serverLog("OpenFile: reading %q failed: %v", path, err)
		return fmt.Errorf("read %s: %w", path, err)
	}

	s.mu.Lock()
	// Re-check before inserting: the lookup above and this insert are no
	// longer one critical section (the read sits between them), so another
	// client opening the same path concurrently could otherwise leave two
	// buffer entries for one file.
	if bufID, existing, ver, gen, ok := s.attachToOpenBufferLocked(path, clientID); ok {
		s.mu.Unlock()
		return setOpenFileResults(call, bufID, existing, ver, gen, false)
	}

	s.nextBuf++
	bufID := s.nextBuf
	buf := document.New(path, content)
	if fromRecovery {
		buf.MarkDirty()
	}
	s.buffers[bufID] = &bufferEntry{
		buf:           buf,
		clients:       map[uint64]struct{}{clientID: {}},
		canonPath:     canonicalPath(path),
		crlf:          crlf,
		sinceByClient: map[uint64]uint64{clientID: buf.Version()},
	}
	ver := buf.Version()
	s.mu.Unlock()

	if err := setOpenFileResults(call, bufID, content, ver, 0, fromRecovery); err != nil {
		return err
	}
	if path != "" {
		go s.lspMgr.DidOpen(path, content)
		go s.pluginMgr.DispatchBufferOpen(context.Background(), bufID, path)
		s.addPathWatch(path)
	}
	return nil
}

// attachToOpenBufferLocked registers clientID as a client of the buffer
// already open for path, if there is one, and returns everything OpenFile
// needs to answer from it. s.mu must be held; the caller unlocks. ok is
// false when nothing is open for path — including always for an untitled
// buffer (path ""), which never dedups.
//
// Content is read here, under the lock, rather than after unlocking as the
// previous inline version did: it costs nothing and closes the window where
// a concurrent wholesale swap of entry.buf (Save's format-on-save branch,
// SaveAs, DiscardRecovery, Format) could be observed mid-swap.
func (s *editorService) attachToOpenBufferLocked(path string, clientID uint64) (
	bufID uint32, content string, ver, gen uint64, ok bool,
) {
	if path == "" {
		return 0, "", 0, 0, false
	}
	canonPath := canonicalPath(path)
	for id, e := range s.buffers {
		if e.canonPath != canonPath {
			continue
		}
		e.clients[clientID] = struct{}{}
		ver = e.buf.Version()
		if e.sinceByClient == nil {
			e.sinceByClient = make(map[uint64]uint64)
		}
		e.sinceByClient[clientID] = ver
		// A client attaching gets the buffer's full current content, so it
		// starts caught up and its queue starts empty. Clearing rather than
		// assuming absence matters for a client that closed this buffer and
		// reopened it: a leftover queue would replay ops against content that
		// already contains them.
		delete(e.outgoing, clientID)
		return id, e.buf.Content(), ver, e.generation, true
	}
	return 0, "", 0, 0, false
}

// setOpenFileResults fills in an openFile response.
func setOpenFileResults(call proto.EditorService_openFile, bufID uint32, content string, ver, gen uint64, fromRecovery bool) error {
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
	res.SetGeneration(gen)
	return nil
}

// RequestOpenFile pushes an open-file command to every connected client,
// exposing the same broadcast Go-native plugins get via
// ServerBridge.PluginOpenFile (plugin_bridge.go) to any client over the wire.
func (s *editorService) RequestOpenFile(_ context.Context, call proto.EditorService_requestOpenFile) error {
	args := call.Args()
	path, err := args.Path()
	if err != nil {
		return err
	}
	if err := s.PluginOpenFile(path, args.Line()); err != nil {
		return err
	}
	_, err = call.AllocResults()
	return err
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
	baseBuf := entry.buf
	baseVersion := baseBuf.Version()
	s.mu.Unlock()

	os.Remove(recoveryFilePath(s.recDir, path)) //nolint:errcheck

	content, crlf := "", false
	if data, err := os.ReadFile(path); err == nil {
		content, crlf = document.NormalizeCRLF(string(data))
	}

	s.mu.Lock()
	entry, ok = s.buffers[bufID]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("unknown buffer %d", bufID)
	}
	if entry.buf != baseBuf || entry.buf.Version() != baseVersion {
		// A concurrent edit landed on the buffer while we were reading the
		// disk content — applying the disk content now would silently
		// discard that edit. Reject rather than clobber it; the caller can
		// retry to pick up the buffer's current state.
		s.mu.Unlock()
		return fmt.Errorf("buffer %d changed while discarding recovery; try again", bufID)
	}
	entry.buf = document.New(path, content)
	entry.crlf = crlf
	entry.generation++
	// Queued ops describe the old buffer object and cannot be rebased onto the
	// new one; clients learn of the swap from the generation bump and resync.
	resetOutgoing(entry)
	generation := entry.generation
	s.mu.Unlock()

	res, err := call.AllocResults()
	if err != nil {
		return err
	}
	// Read under the lock above, not re-read here: entry.generation is guarded
	// by s.mu and every other swap site writes it.
	res.SetGeneration(generation)
	return res.SetContent(content)
}

// ReloadBuffer re-reads the buffer's file from disk and replaces its content,
// bumping generation so every other client holding it resyncs on its next poll.
//
// The client-side spelling this replaces — CloseBuffer then OpenFile — is
// broken whenever a second window has the file open: CloseBuffer only drops the
// calling client, so the entry survives and OpenFile's attach path serves the
// same in-memory content back without reading disk. See editor.capnp.
func (s *editorService) ReloadBuffer(_ context.Context, call proto.EditorService_reloadBuffer) error {
	// Disk reads can stall; call.Go() so a slow one doesn't block other RPCs
	// on this connection, matching Save/SaveAs/Format.
	call.Go()

	bufID := call.Args().BufferId()

	s.mu.Lock()
	entry, ok := s.buffers[bufID]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("unknown buffer %d", bufID)
	}
	baseBuf := entry.buf
	path := baseBuf.Path()
	baseVersion := baseBuf.Version()
	s.mu.Unlock()

	if path == "" {
		return fmt.Errorf("buffer %d has no file to reload", bufID)
	}

	// A read failure is reported, not swallowed into an empty buffer. Reload's
	// whole job is to replace live content, so treating an unreadable file as
	// "" would blank the user's buffer on a transient error — during someone
	// else's atomic save, say — which is far worse than declining to reload.
	data, err := os.ReadFile(path)
	if err != nil {
		serverLog("ReloadBuffer: reading %q failed: %v", path, err)
		return fmt.Errorf("read %s: %w", path, err)
	}
	content, crlf := document.NormalizeCRLF(string(data))

	s.mu.Lock()
	entry, ok = s.buffers[bufID]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("unknown buffer %d", bufID)
	}
	if entry.buf != baseBuf || entry.buf.Version() != baseVersion {
		// An edit landed while we were reading, so swapping in the disk
		// content now would discard it. Reject rather than clobber; the caller
		// can retry. Same compare-and-swap as DiscardRecovery/Save/SaveAs, and
		// both halves matter: document.New always starts at version 0, so a
		// buffer swapped in by something else could match baseVersion by
		// coincidence while being a different object entirely.
		s.mu.Unlock()
		return fmt.Errorf("buffer %d changed while reloading; try again", bufID)
	}
	entry.buf = document.New(path, content)
	entry.crlf = crlf
	entry.generation++
	resetOutgoing(entry)
	version := entry.buf.Version()
	generation := entry.generation
	s.mu.Unlock()

	// The reload deliberately discards whatever was in memory, so a recovery
	// file still holding it must go too — otherwise the next open would offer
	// to restore the very content the user just chose to throw away.
	os.Remove(recoveryFilePath(s.recDir, path)) //nolint:errcheck

	// DidChange/DispatchBufferChange rather than the close+open pair the old
	// client-side flow produced: the document is the same one, its content
	// just changed wholesale, which is exactly what a full-sync didChange
	// says. No watch churn either, since the path never changed.
	go s.lspMgr.DidChange(path, content)
	s.lintMgr.RunOnEdit(path, content)
	go s.pluginMgr.DispatchBufferChange(context.Background(), bufID, path)

	res, err := call.AllocResults()
	if err != nil {
		return err
	}
	res.SetVersion(version)
	res.SetGeneration(generation)
	return res.SetContent(content)
}

// loadContent reads a file's content, preferring a newer recovery file if one
// exists.
//
// A read failure other than "no such file" is reported rather than swallowed:
// falling through with empty content would open an empty buffer for a file
// that actually has content — misrepresenting the file, and risking
// overwriting it wholesale on the next save. "No such file" is not an error
// here: that's the ordinary new-file case (and a file that's since been
// deleted may still have a recovery file worth replaying), so it keeps
// falling through to the recovery check with empty content.
func (s *editorService) loadContent(path string) (content string, fromRecovery, crlf bool, err error) {
	if path == "" {
		return "", false, false, nil // untitled buffer: no content, no recovery
	}
	var origModTime time.Time
	if info, statErr := os.Stat(path); statErr == nil {
		origModTime = info.ModTime()
	}
	data, readErr := os.ReadFile(path)
	switch {
	case readErr == nil:
		// The line-ending style is the file's, even when the content below
		// comes from a recovery snapshot: it describes how to write back.
		content, crlf = document.NormalizeCRLF(string(data))
	case !os.IsNotExist(readErr):
		return "", false, false, readErr
	}
	rp := recoveryFilePath(s.recDir, path)
	if recInfo, statErr := os.Stat(rp); statErr == nil && recInfo.ModTime().After(origModTime) {
		if recData, recErr := os.ReadFile(rp); recErr == nil {
			// Snapshots are written from the buffer, so already "\n"; one
			// from before line endings were normalized may not be.
			rec, _ := document.NormalizeCRLF(string(recData))
			return rec, true, crlf, nil
		}
	}
	return content, false, crlf, nil
}

func (s *editorService) GetUpdates(_ context.Context, call proto.EditorService_getUpdates) error {
	args := call.Args()
	bufID := args.BufferId()
	since := args.SinceVersion()
	callerID := args.ClientId()

	// Everything is snapshotted under the lock, including the queue itself:
	// entry.buf, entry.generation and entry.outgoing are all written under s.mu
	// by applies and by every wholesale-swap site, so reading any of them after
	// unlocking races them. Taking them together also keeps the response
	// internally consistent — ops and generation describing the same buffer
	// object rather than two different ones straddling a swap.
	s.mu.Lock()
	entry, ok := s.buffers[bufID]
	var buf *document.Buffer
	var generation, ver, appliedFromCaller uint64
	var pending []document.Op
	if ok {
		buf, generation = entry.buf, entry.generation
		appliedFromCaller = entry.appliedFromClient[callerID]
		// ver is read here, inside the same critical section as the queue
		// snapshot, and not afterwards. The client treats it as "I now hold
		// everything up to this version", so a concurrent apply landing between
		// the two reads would have it adopt a version covering an op that was
		// never in the response — and since the watermark only moves forward,
		// that op would never be sent again. Silent, permanent desync.
		ver = buf.Version()
		if entry.outgoing != nil {
			// Ops this client has now confirmed seeing are dropped here rather
			// than on delivery: a response that never arrives must not retire
			// ops the client will ask for again.
			entry.outgoing[callerID] = dropAcknowledged(entry.outgoing[callerID], since)
			recordPruned(entry, callerID, since)
			pending = append(pending, entry.outgoing[callerID]...)
		}
	}
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("unknown buffer %d", bufID)
	}

	// An injected drop must be *recoverable*, which takes more than returning
	// no ops. Reporting ver with an empty list would have the client adopt a
	// version covering ops it never received, and since the watermark only
	// moves forward those ops would never be sent again — permanent loss, not a
	// lost response. So the response also reports the version the caller
	// already had, and progress is not recorded: the queue keeps the ops (their
	// version is still above `since`) and the next poll delivers them. From the
	// client this is indistinguishable from a poll that ran a moment earlier.
	//
	// Returning an error instead would exercise the RPC-failure path, which is
	// a different fault and already reachable by stopping the server.
	droppedPoll := faultinject.ShouldDropPoll()
	if droppedPoll {
		serverLog("GetUpdates: dropping %d op(s) for buffer %d (fault injection)", len(pending), bufID)
		pending = nil
		ver = since
	}

	res, err := call.AllocResults()
	if err != nil {
		return err
	}
	res.SetVersion(ver)
	res.SetGeneration(generation)
	res.SetAppliedFromCaller(appliedFromCaller)
	// Saved-content hash lets clients keep their dirty markers accurate when
	// another client (e.g. an agent) saves the buffer.
	h := buf.SavedHash()
	if err := res.SetSavedHash(h[:]); err != nil {
		return err
	}

	if len(pending) == 0 {
		if droppedPoll {
			// Deliberately no recordClientProgress: a dropped response must not
			// advance what the server believes this client has seen, or
			// TrimHistory could reclaim the very ops the retry needs.
			return nil
		}
		// recordClientProgress only after the response is fully built. It
		// advances this client's watermark to ver, which is what lets
		// TrimHistory reclaim buffer history — so recording it before the
		// response exists means a failure below (or a response that never
		// reaches the client) can retire ops the client never received.
		s.recordClientProgress(entry, callerID, ver)
		return nil
	}

	list, err := res.NewOps(int32(len(pending)))
	if err != nil {
		return err
	}
	for i, op := range pending {
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
	// See the note on the early return above: the watermark moves only once
	// the whole response is encoded.
	s.recordClientProgress(entry, callerID, ver)
	return nil
}

// historyTrimThreshold bounds how many ops a buffer's history is allowed to
// accumulate before recordClientProgress reclaims already-acknowledged
// entries. Kept well above a normal burst of edits so trimming — which
// reallocates the retained slice — stays rare rather than running on every
// keystroke.
const historyTrimThreshold = 500

// recordClientProgress records that clientID has now caught up to version
// ver of entry's buffer (via GetUpdates, ApplyOp, or ApplyOps), then — once
// the buffer's retained op history has grown past historyTrimThreshold —
// reclaims every op that all currently connected clients have already seen.
//
// Without this, document.Buffer.history grows for the entire life of a long
// editing session even though old ops stop being useful the moment every
// client has moved past them: a buffer only gets cleared out when its last
// client closes it (see CloseBuffer), which can be hours away.
func (s *editorService) recordClientProgress(entry *bufferEntry, clientID, ver uint64) {
	s.mu.Lock()
	if entry.sinceByClient == nil {
		entry.sinceByClient = make(map[uint64]uint64)
	}
	entry.sinceByClient[clientID] = ver
	needsTrim := entry.buf.HistoryLen() > historyTrimThreshold
	min := ver
	if needsTrim {
		for id := range entry.clients {
			v, ok := entry.sinceByClient[id]
			if !ok {
				v = 0
			}
			if v < min {
				min = v
			}
		}
	}
	s.mu.Unlock()
	if needsTrim {
		entry.buf.TrimHistory(min)
	}
}

// GetBufferSnapshot returns a buffer's current authoritative content by ID.
// Used by clients resyncing after a failed ApplyOp or a detected generation
// mismatch — unlike OpenFile, this is keyed by bufferId, not path, so it
// still finds the right buffer even if the client's own remembered path is
// stale (e.g. a different client renamed it via SaveAs since this one last
// synced).
func (s *editorService) GetBufferSnapshot(_ context.Context, call proto.EditorService_getBufferSnapshot) error {
	bufID := call.Args().BufferId()

	s.mu.Lock()
	entry, ok := s.buffers[bufID]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("unknown buffer %d", bufID)
	}
	content := entry.buf.Content()
	version := entry.buf.Version()
	generation := entry.generation
	path := entry.buf.Path()
	s.mu.Unlock()

	res, err := call.AllocResults()
	if err != nil {
		return err
	}
	if err := res.SetContent(content); err != nil {
		return err
	}
	res.SetVersion(version)
	res.SetGeneration(generation)
	return res.SetPath(path)
}

// protoToOp converts a wire EditOp into a document.Op for the given client.
func protoToOp(protoOp proto.EditOp, clientID uint64) document.Op {
	insertText, _ := protoOp.InsertText()
	expectText, _ := protoOp.ExpectText()
	op := document.Op{
		ClientID:   clientID,
		InsertLine: int(protoOp.InsertLine()),
		InsertCol:  int(protoOp.InsertCol()),
		InsertText: insertText,
		ExpectText: expectText,
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
	return op
}

func (s *editorService) ApplyOp(_ context.Context, call proto.EditorService_applyOp) error {
	args := call.Args()
	clientID := args.ClientId()
	bufID := args.BufferId()
	clientGeneration := args.Generation()
	baseVersion := args.BaseVersion()
	protoOp, err := args.Op()
	if err != nil {
		return err
	}
	op := protoToOp(protoOp, clientID)

	// Lookup, generation check, rebase, apply and broadcast are one critical
	// section. They have to be: the op's assigned version and the rewritten
	// client queues are a single consistent step, and two concurrent applies
	// interleaving between them would leave a queue describing a document state
	// that never existed. It also closes the older gap where the generation was
	// validated in one lock acquisition and the op applied after it.
	s.mu.Lock()
	entry, ok := s.buffers[bufID]
	if !ok {
		s.mu.Unlock()
		serverLog("ApplyOp REJECTED: unknown buffer %d (client %d) — the buffer was closed or "+
			"the server restarted while a client still held it", bufID, clientID)
		syncevent.Recordf("server", syncevent.ApplyOpRejected, bufID, "",
			"unknown buffer (client %d)", clientID)
		return fmt.Errorf("unknown buffer %d", bufID)
	}
	// Compiled out entirely unless -tags indigo_debug; see internal/faultinject.
	// Placed before the real generation check so an injected bump is
	// indistinguishable from a genuine wholesale swap — the client must not be
	// able to tell, or the path being exercised is not the real one.
	if faultinject.ShouldBumpGeneration() {
		entry.generation++
	}
	if faultinject.ShouldFailApplyOp() {
		path := entry.buf.Path()
		s.mu.Unlock()
		serverLog("ApplyOp REJECTED: buffer %d (%q) fault injection", bufID, path)
		syncevent.Record("server", syncevent.ApplyOpRejected, bufID, path, "fault injection")
		return fmt.Errorf("buffer %d: injected failure", bufID)
	}
	if entry.generation != clientGeneration {
		gen, path := entry.generation, entry.buf.Path()
		s.mu.Unlock()
		// Logged, not just returned. A rejection here makes the client discard
		// the edit and resync, which the user sees as an error modal — and the
		// error string below was the only record that it happened, living just
		// long enough to be overwritten on screen. The server is the one place
		// that knows *why*, so it says so somewhere durable.
		// %q, not %s: the path traces back to a client-supplied OpenFile
		// argument, and the log is line-oriented — an embedded newline would
		// let a caller write whatever it liked as a separate log line.
		serverLog("ApplyOp REJECTED: buffer %d (%q) generation mismatch: client has %d, server has %d",
			bufID, path, clientGeneration, gen)
		syncevent.Recordf("server", syncevent.GenerationMismatch, bufID, path,
			"applyOp from client %d: client has %d, server has %d", clientID, clientGeneration, gen)
		return fmt.Errorf("buffer %d generation mismatch: client has %d, server has %d", bufID, clientGeneration, gen)
	}
	buf := entry.buf
	applied, newVersion, rebaseErr := applyRebased(entry, buf, clientID, baseVersion, []document.Op{op})
	path := buf.Path()
	content := buf.Content()
	s.mu.Unlock()

	if rebaseErr != nil {
		serverLog("ApplyOp REJECTED: buffer %d (%q) %v", bufID, path, rebaseErr)
		syncevent.Recordf("server", syncevent.StaleBase, bufID, path,
			"applyOp from client %d: %v", clientID, rebaseErr)
		return fmt.Errorf("buffer %d: %w", bufID, rebaseErr)
	}

	s.recordClientProgress(entry, clientID, newVersion)

	res, err := call.AllocResults()
	if err != nil {
		return err
	}
	res.SetVersion(newVersion)

	go s.lspMgr.DidChange(path, content)
	go s.pluginMgr.DispatchBufferChange(context.Background(), bufID, path)
	s.dispatchLineDeltas(bufID, path, applied)
	return nil
}

// applyRebased rebases ops into the server's current context, applies them,
// stamps each with the version it was applied at, and queues them for every
// other client. It returns what was actually applied and the buffer's resulting
// version. Callers must hold s.mu.
//
// The applied slice can be empty even for a non-empty input: an op whose every
// deleted character another client had already deleted rebases away to nothing.
// That is success, not failure — the op's intent is already satisfied — so the
// version returned is simply the buffer's unchanged current one.
func applyRebased(entry *bufferEntry, buf *document.Buffer, clientID, baseVersion uint64, ops []document.Op) ([]document.Op, uint64, error) {
	rebased, err := rebaseIncoming(entry, clientID, baseVersion, ops)
	if err != nil {
		// Counted only once the op is actually going to be applied: a rejected
		// op never reaches the buffer, and reporting it as applied would have
		// the client retire a pending entry that the server does not have.
		return nil, buf.Version(), err
	}
	if err := verifyExpectedText(buf, ops, rebased); err != nil {
		// Counted only once the batch is actually going to be applied, same as
		// the rebase rejection above: a refused batch never reaches the buffer.
		return nil, buf.Version(), err
	}
	recordAppliedFrom(entry, clientID, len(ops))
	applied := make([]document.Op, 0, len(rebased))
	newVersion := buf.Version()
	for _, r := range rebased {
		// Version is stamped onto our copy as well as the buffer's history:
		// the queues use it to decide what a client has acknowledged, and it
		// has to survive any later rebasing of the queued op.
		v := buf.Apply(r)
		r.Version = v
		newVersion = v
		applied = append(applied, r)
	}
	broadcast(entry, clientID, applied)
	return applied, newVersion, nil
}

// dispatchLineDeltas notifies plugin edit-event handlers for any op that
// changed the line count.
func (s *editorService) dispatchLineDeltas(bufID uint32, path string, ops []document.Op) {
	for _, op := range ops {
		var lineDelta int32
		var atLine uint32
		switch op.Type {
		case document.OpInsert:
			if delta := int32(strings.Count(op.InsertText, "\n")); delta != 0 {
				lineDelta = delta
				atLine = uint32(op.InsertLine)
			}
		case document.OpDelete:
			if delta := int32(op.FromLine - op.ToLine); delta != 0 { // negative: lines removed
				lineDelta = delta
				atLine = uint32(op.FromLine)
			}
		}
		if lineDelta != 0 {
			go s.pluginMgr.DispatchEditEvent(context.Background(), bufID, path, atLine, lineDelta)
		}
	}
}

// ApplyOps applies a batch of ops back-to-back. The atomicity guarantee is at
// the request level: once the call arrives, every op is applied even if the
// client disconnects mid-request — so a delete+insert pair can never be left
// half-done by a client crash.
func (s *editorService) ApplyOps(_ context.Context, call proto.EditorService_applyOps) error {
	args := call.Args()
	clientID := args.ClientId()
	bufID := args.BufferId()
	clientGeneration := args.Generation()
	baseVersion := args.BaseVersion()
	protoOps, err := args.Ops()
	if err != nil {
		return err
	}
	ops := make([]document.Op, protoOps.Len())
	for i := range ops {
		ops[i] = protoToOp(protoOps.At(i), clientID)
	}

	// One critical section, for the same reason as ApplyOp's — and here it also
	// delivers the batch's stated guarantee, that a paired delete+insert can
	// never be observed half-applied.
	s.mu.Lock()
	entry, ok := s.buffers[bufID]
	if !ok {
		s.mu.Unlock()
		// Same reasoning as ApplyOp's rejections: the caller (an agent tool, or
		// a client replaying a batch) gets a string it may not surface, and this
		// is the only place that knows it happened.
		serverLog("ApplyOps REJECTED: unknown buffer %d (client %d)", bufID, clientID)
		syncevent.Recordf("server", syncevent.ApplyOpRejected, bufID, "",
			"applyOps: unknown buffer (client %d)", clientID)
		return fmt.Errorf("unknown buffer %d", bufID)
	}
	if faultinject.ShouldBumpGeneration() {
		entry.generation++
	}
	if faultinject.ShouldFailApplyOp() {
		path := entry.buf.Path()
		s.mu.Unlock()
		serverLog("ApplyOps REJECTED: buffer %d (%q) fault injection", bufID, path)
		syncevent.Record("server", syncevent.ApplyOpRejected, bufID, path, "fault injection")
		return fmt.Errorf("buffer %d: injected failure", bufID)
	}
	if entry.generation != clientGeneration {
		gen, path := entry.generation, entry.buf.Path()
		s.mu.Unlock()
		serverLog("ApplyOps REJECTED: buffer %d (%q) generation mismatch: client has %d, server has %d",
			bufID, path, clientGeneration, gen)
		syncevent.Recordf("server", syncevent.GenerationMismatch, bufID, path,
			"applyOps from client %d: client has %d, server has %d", clientID, clientGeneration, gen)
		return fmt.Errorf("buffer %d generation mismatch: client has %d, server has %d", bufID, clientGeneration, gen)
	}
	buf := entry.buf
	applied, newVersion, rebaseErr := applyRebased(entry, buf, clientID, baseVersion, ops)
	path := buf.Path()
	content := buf.Content()
	s.mu.Unlock()

	if rebaseErr != nil {
		serverLog("ApplyOps REJECTED: buffer %d (%q) %v", bufID, path, rebaseErr)
		syncevent.Recordf("server", syncevent.StaleBase, bufID, path,
			"applyOps from client %d: %v", clientID, rebaseErr)
		return fmt.Errorf("buffer %d: %w", bufID, rebaseErr)
	}

	if len(ops) > 0 {
		s.recordClientProgress(entry, clientID, newVersion)
	}

	res, err := call.AllocResults()
	if err != nil {
		return err
	}
	res.SetVersion(newVersion)

	go s.lspMgr.DidChange(path, content)
	s.lintMgr.RunOnEdit(path, content)
	go s.pluginMgr.DispatchBufferChange(context.Background(), bufID, path)
	s.dispatchLineDeltas(bufID, path, applied)
	return nil
}

func (s *editorService) Save(_ context.Context, call proto.EditorService_save) error {
	// Save can block on a synchronous format call (up to 10s — see Format
	// above) plus a disk write; call.Go() so a slow save doesn't freeze
	// every other RPC on this connection (typing, etc.) behind it.
	call.Go()

	args := call.Args()
	bufID := args.BufferId()

	s.mu.Lock()
	entry, ok := s.buffers[bufID]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("unknown buffer %d", bufID)
	}
	path := entry.buf.Path()
	baseBuf := entry.buf
	baseVersion := baseBuf.Version()
	content := baseBuf.Content()
	crlf := entry.crlf
	s.mu.Unlock()

	if s.cfg.FormatOnSave {
		if formatted, changed, err := s.fmtMgr.Format(path, content); err == nil && changed {
			// A formatter may emit "\r\n" (prettier's endOfLine, say); the
			// buffer must not hold it. If it did, the file is CRLF now.
			formatted, fmtCRLF := document.NormalizeCRLF(formatted)
			crlf = crlf || fmtCRLF
			s.mu.Lock()
			entry, ok = s.buffers[bufID]
			// Same compare-and-swap Format() uses: only apply the formatted
			// result if the buffer is still the exact object/version we
			// formatted. Otherwise a concurrent edit landed while the
			// formatter (an external process, up to 10s) was running.
			if ok && entry.buf == baseBuf && entry.buf.Version() == baseVersion {
				newBuf := document.New(path, formatted)
				newBuf.MarkDirty()
				entry.buf = newBuf
				entry.crlf = crlf
				entry.generation++
				resetOutgoing(entry)
				baseBuf = newBuf
				baseVersion = newBuf.Version()
				content = formatted
				s.mu.Unlock()
				go s.lspMgr.DidChange(path, formatted)
			} else if ok {
				// Discard the stale formatted result rather than clobbering
				// the newer edit — save the buffer's actual current state.
				baseBuf = entry.buf
				baseVersion = baseBuf.Version()
				content = baseBuf.Content()
				// And its line endings: the buffer may have been swapped
				// (a reload) since crlf was read, and the formatter's
				// CRLF-ness belongs to the discarded result, not this one.
				crlf = entry.crlf
				s.mu.Unlock()
			} else {
				s.mu.Unlock()
				return fmt.Errorf("unknown buffer %d", bufID)
			}
		}
	}

	s.markSaving(path)
	if err := atomicWriteFile(path, []byte(document.RestoreCRLF(content, crlf)), 0644); err != nil {
		s.unmarkSaving(path)
		return err
	}
	s.unmarkSaving(path)

	recoveryPath := recoveryFilePath(s.recDir, path)
	s.mu.Lock()
	entry, ok = s.buffers[bufID]
	switch {
	case ok && entry.buf == baseBuf && entry.buf.Version() == baseVersion:
		// What's on disk matches the buffer's content — safe to mark clean
		// and drop the recovery file.
		entry.buf.SetClean()
		s.mu.Unlock()
		os.Remove(recoveryPath) //nolint:errcheck
	case ok:
		// The buffer changed again during the disk write (or a concurrent
		// format-on-save race) — leave it dirty, since what's on disk no
		// longer matches its current content, and refresh the recovery
		// file immediately with the current content rather than leaving a
		// stale-or-absent one until the next periodic flushDirtyBuffers
		// tick, so a crash right now doesn't lose the newer edit.
		current := entry.buf.Content()
		within := int64(entry.buf.ByteLen()) <= s.cfg.RecoveryMaxBytes
		s.mu.Unlock()
		if within {
			os.WriteFile(recoveryPath, []byte(current), 0600) //nolint:errcheck
		} else {
			os.Remove(recoveryPath) //nolint:errcheck
		}
	default:
		// Buffer was closed entirely during the write.
		s.mu.Unlock()
		os.Remove(recoveryPath) //nolint:errcheck
	}

	go s.lspMgr.DidSave(path)
	s.lintMgr.RunAsync(path, content)
	go s.pluginMgr.DispatchBufferSave(context.Background(), bufID, path)

	_, err := call.AllocResults()
	return err
}

func (s *editorService) SaveAs(_ context.Context, call proto.EditorService_saveAs) error {
	// Disk I/O to a new path can stall briefly; call.Go() so it doesn't
	// block other RPCs on this connection behind it, matching Save/Format.
	call.Go()

	args := call.Args()
	bufID := args.BufferId()
	newPath, err := args.Path()
	if err != nil {
		return err
	}

	s.mu.Lock()
	entry, ok := s.buffers[bufID]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("unknown buffer %d", bufID)
	}
	baseBuf := entry.buf
	baseVersion := baseBuf.Version()
	content := baseBuf.Content()
	oldPath := baseBuf.Path()
	// A copy keeps the buffer's line endings: Save As is "this content,
	// elsewhere", and the content includes how its lines end.
	crlf := entry.crlf
	s.mu.Unlock()

	s.markSaving(newPath)
	if err := atomicWriteFile(newPath, []byte(document.RestoreCRLF(content, crlf)), 0o644); err != nil {
		s.unmarkSaving(newPath)
		return err
	}
	s.unmarkSaving(newPath)

	s.mu.Lock()
	entry, ok = s.buffers[bufID]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("unknown buffer %d", bufID)
	}
	if entry.buf != baseBuf || entry.buf.Version() != baseVersion {
		// A concurrent edit landed on the buffer while the write to newPath
		// was in flight, so newPath now holds a stale snapshot of the
		// buffer's content. Don't repoint the live (newer) buffer at it —
		// that would silently discard the concurrent edit. The file at
		// newPath is left as-is; the caller should retry SaveAs to pick up
		// the buffer's current content.
		s.mu.Unlock()
		return fmt.Errorf("buffer changed while saving to %s; try again", newPath)
	}
	if oldPath != newPath {
		s.removePathWatch(oldPath)
		s.addPathWatch(newPath)
	}
	entry.buf = document.New(newPath, content)
	resetOutgoing(entry)
	entry.buf.SetClean()
	entry.canonPath = canonicalPath(newPath)
	entry.generation++
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
		delete(entry.sinceByClient, clientID)
		delete(entry.outgoing, clientID)
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
		s.removePathWatch(removedPath)
		go s.lspMgr.DidClose(removedPath)
		s.lintMgr.Forget(removedPath)
		go s.pluginMgr.DispatchBufferClose(context.Background(), bufID, removedPath)
	}

	_, err := call.AllocResults()
	return err
}
