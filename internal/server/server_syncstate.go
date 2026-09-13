package server

import (
	"context"
	"crypto/sha256"
	"sort"

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
