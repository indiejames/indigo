package server

import (
	"context"
	"crypto/sha256"
	"sort"
	"time"

	proto "github.com/indiejames/indigo/internal/proto"
)

// GetSyncState reports the server's buffer-synchronization bookkeeping, for
// diagnosing sync problems. Read-only: every field it returns is already
// tracked for its own reasons.
//
// bufferId 0 means every open buffer. Content is reported only as a sha256 and
// a byte count — this is the call whose output ends up pasted into a bug
// report, and it must never be the thing that leaks someone's source.
func (s *editorService) GetSyncState(_ context.Context, call proto.EditorService_getSyncState) error {
	want := call.Args().BufferId()

	// Everything is snapshotted under the lock and formatted after, so the
	// capnp allocation below can't run while holding s.mu.
	type clientSnap struct {
		clientID, acked, connID uint64
	}
	type bufSnap struct {
		bufID                   uint32
		path                    string
		version, generation     uint64
		dirty                   bool
		sum                     [sha256.Size]byte
		contentBytes, lineCount uint64
		historyLen              uint32
		clients                 []clientSnap
	}

	var snaps []bufSnap
	s.mu.Lock()
	for bufID, e := range s.buffers {
		if want != 0 && bufID != want {
			continue
		}
		content := e.buf.Content()
		b := bufSnap{
			bufID:        bufID,
			path:         e.buf.Path(),
			version:      e.buf.Version(),
			generation:   e.generation,
			dirty:        e.buf.Dirty(),
			sum:          sha256.Sum256([]byte(content)),
			contentBytes: uint64(len(content)),
			lineCount:    uint64(e.buf.LineCount()),
			historyLen:   uint32(e.buf.HistoryLen()),
		}
		for clientID := range e.clients {
			cs := clientSnap{clientID: clientID, acked: e.sinceByClient[clientID]}
			if ce, ok := s.clientMap[clientID]; ok {
				cs.connID = ce.connID
			}
			b.clients = append(b.clients, cs)
		}
		// Map iteration order is random; a diagnostic whose rows shuffle
		// between calls is painful to diff against a previous report.
		sort.Slice(b.clients, func(i, j int) bool { return b.clients[i].clientID < b.clients[j].clientID })
		snaps = append(snaps, b)
	}
	s.mu.Unlock()
	sort.Slice(snaps, func(i, j int) bool { return snaps[i].bufID < snaps[j].bufID })

	res, err := call.AllocResults()
	if err != nil {
		return err
	}
	list, err := res.NewBuffers(int32(len(snaps)))
	if err != nil {
		return err
	}
	for i, b := range snaps {
		item := list.At(i)
		item.SetBufferId(b.bufID)
		if err := item.SetPath(b.path); err != nil {
			return err
		}
		item.SetVersion(b.version)
		item.SetGeneration(b.generation)
		item.SetDirty(b.dirty)
		sum := b.sum
		if err := item.SetContentSha256(sum[:]); err != nil {
			return err
		}
		item.SetContentBytes(b.contentBytes)
		item.SetLineCount(uint32(b.lineCount))
		item.SetHistoryLen(b.historyLen)

		clients, err := item.NewClients(int32(len(b.clients)))
		if err != nil {
			return err
		}
		for j, c := range b.clients {
			ci := clients.At(j)
			ci.SetClientId(c.clientID)
			ci.SetAckedVersion(c.acked)
			ci.SetConnId(c.connID)
		}
	}
	return nil
}

// consistencyCallTimeout bounds one client's answer. Deliberately short: a
// consistency check is a diagnostic, and a window that cannot answer in this
// long is itself the finding.
const consistencyCallTimeout = 500 * time.Millisecond

// consistencyCollectSlack is how much longer than one call's timeout the
// collector waits before giving up on stragglers. Small, but non-zero: without
// it a client answering right on the deadline would race the timer and be
// reported as unresponsive when it in fact replied.
const consistencyCollectSlack = 250 * time.Millisecond

// CheckBufferConsistency asks every client holding a buffer what it actually
// holds, and reports each answer alongside the server's own view.
//
// It reports facts, not a verdict — see editor.capnp. A hash mismatch is normal
// mid-edit, since a client applies its own edit locally before the server
// orders it; distinguishing that from real divergence needs two samples, which
// is the caller's job.
func (s *editorService) CheckBufferConsistency(ctx context.Context, call proto.EditorService_checkBufferConsistency) error {
	// The per-client callbacks below are network round trips; call.Go() so they
	// don't block other RPCs on this connection.
	call.Go()

	want := call.Args().BufferId()

	type target struct {
		bufID      uint32
		path       string
		version    uint64
		generation uint64
		sum        [sha256.Size]byte
		clientIDs  []uint64
		callbacks  map[uint64]proto.ClientCallback
	}

	var targets []target
	s.mu.Lock()
	for bufID, e := range s.buffers {
		if want != 0 && bufID != want {
			continue
		}
		t := target{
			bufID:      bufID,
			path:       e.buf.Path(),
			version:    e.buf.Version(),
			generation: e.generation,
			sum:        sha256.Sum256([]byte(e.buf.Content())),
			callbacks:  map[uint64]proto.ClientCallback{},
		}
		for clientID := range e.clients {
			t.clientIDs = append(t.clientIDs, clientID)
			if ce, ok := s.clientMap[clientID]; ok && ce.callback.IsValid() {
				t.callbacks[clientID] = ce.callback
			}
		}
		sort.Slice(t.clientIDs, func(i, j int) bool { return t.clientIDs[i] < t.clientIDs[j] })
		targets = append(targets, t)
	}
	s.mu.Unlock()
	sort.Slice(targets, func(i, j int) bool { return targets[i].bufID < targets[j].bufID })

	type answer struct {
		answered, known, dirty bool
		version, generation    uint64
		sum                    []byte
	}

	res, err := call.AllocResults()
	if err != nil {
		return err
	}
	list, err := res.NewBuffers(int32(len(targets)))
	if err != nil {
		return err
	}

	for i, t := range targets {
		// Query this buffer's clients concurrently, each with its own timeout —
		// the convention PluginDecorationsChanged established. Serially with a
		// shared context, one wedged window would eat the whole budget and make
		// every other client look unresponsive too.
		//
		// Collection is bounded by its own timer rather than by waiting for
		// every goroutine, because a per-call context is *not* enough to
		// guarantee they finish. Verified against this capnp version: when a
		// callee never returns, cancelling the caller's context does not
		// resolve the future, so fut.Struct() can block past its deadline. A
		// real client's handler does respect the deadline, but a client whose
		// process is stopped never runs one at all — and a diagnostic that
		// hangs when a window is wedged is useless precisely when it matters.
		//
		// Results arrive on a buffered channel and only this goroutine writes
		// answers, so a straggler landing after the deadline is harmless rather
		// than a data race on the slice.
		answers := make([]answer, len(t.clientIDs))
		type indexed struct {
			j int
			a answer
		}
		ch := make(chan indexed, len(t.clientIDs))
		launched := 0
		for j, clientID := range t.clientIDs {
			cb, ok := t.callbacks[clientID]
			if !ok {
				continue // registered as a client but has no valid callback
			}
			launched++
			go func(j int, cb proto.ClientCallback) {
				cctx, cancel := context.WithTimeout(ctx, consistencyCallTimeout)
				defer cancel()
				fut, rel := cb.ReportBufferState(cctx, func(p proto.ClientCallback_reportBufferState_Params) error {
					p.SetBufId(t.bufID)
					return nil
				})
				defer rel()
				r, err := fut.Struct()
				if err != nil {
					ch <- indexed{j: j} // answered stays false
					return
				}
				sum, _ := r.ContentSha256()
				ch <- indexed{j: j, a: answer{
					answered:   true,
					known:      r.Known(),
					dirty:      r.Dirty(),
					version:    r.Version(),
					generation: r.Generation(),
					sum:        append([]byte(nil), sum...),
				}}
			}(j, cb)
		}

		deadline := time.After(consistencyCallTimeout + consistencyCollectSlack)
	collect:
		for got := 0; got < launched; got++ {
			select {
			case r := <-ch:
				answers[r.j] = r.a
			case <-ctx.Done():
				// The collect deadline alone is not enough. It is per buffer, so
				// a cancelled caller would still wait it out once for every
				// buffer in the report before returning. The launched goroutines
				// are left to finish into the buffered channel, as they already
				// are on the deadline path.
				return ctx.Err()
			case <-deadline:
				break collect // whoever hasn't answered is reported as not having
			}
		}

		item := list.At(i)
		item.SetBufferId(t.bufID)
		if err := item.SetPath(t.path); err != nil {
			return err
		}
		item.SetServerVersion(t.version)
		item.SetServerGeneration(t.generation)
		sum := t.sum
		if err := item.SetServerSha256(sum[:]); err != nil {
			return err
		}
		clients, err := item.NewClients(int32(len(t.clientIDs)))
		if err != nil {
			return err
		}
		for j, clientID := range t.clientIDs {
			ci := clients.At(j)
			ci.SetClientId(clientID)
			a := answers[j]
			ci.SetAnswered(a.answered)
			ci.SetKnown(a.known)
			ci.SetVersion(a.version)
			ci.SetGeneration(a.generation)
			ci.SetDirty(a.dirty)
			if a.sum != nil {
				if err := ci.SetContentSha256(a.sum); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
