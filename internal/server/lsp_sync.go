package server

import "sync"

// lspEventKind is one document-sync notification to a language server.
type lspEventKind uint8

const (
	lspOpen lspEventKind = iota
	lspChange
	lspClose
)

// lspQueue delivers didOpen/didChange/didClose to the language servers one
// path at a time, in the order they were pushed.
//
// Each used to be its own goroutine (`go s.lspMgr.DidChange(path, content)`),
// so two edits in quick succession could reach the language server in either
// order. The client numbers versions as it sends, so the older content then
// arrived with the newer version and the server kept it: diagnostics,
// completion and formatting were computed against text the buffer no longer
// held, until the next edit happened to put it right.
//
// A change carries no content. The content is read from the buffer when the
// notification is sent, so whatever order the sends happen in, the last one
// carries the current text; and changes waiting behind one another collapse
// into a single send.
type lspQueue struct {
	mu sync.Mutex
	// pending holds each path's unsent events. A path is present while a
	// goroutine is draining it, even with nothing left to send.
	pending map[string][]lspEventKind
}

// notifyLSP queues a notification for path. Callers that change a buffer
// should call it while still holding s.mu, so the queue order matches the
// order the buffer table changed in — that is what keeps a didClose from
// overtaking the didOpen of a buffer reopened straight after.
func (s *editorService) notifyLSP(kind lspEventKind, path string) {
	if path == "" {
		return
	}
	q := &s.lspQ
	q.mu.Lock()
	defer q.mu.Unlock()
	events, draining := q.pending[path]
	if kind == lspChange && len(events) > 0 && events[len(events)-1] == lspChange {
		return // the queued change will read the content this one would have
	}
	if q.pending == nil {
		q.pending = make(map[string][]lspEventKind)
	}
	q.pending[path] = append(events, kind)
	if !draining {
		go s.drainLSP(path)
	}
}

func (s *editorService) drainLSP(path string) {
	q := &s.lspQ
	for {
		q.mu.Lock()
		events := q.pending[path]
		if len(events) == 0 {
			delete(q.pending, path)
			q.mu.Unlock()
			return
		}
		kind := events[0]
		q.pending[path] = events[1:]
		q.mu.Unlock()
		s.sendLSP(kind, path)
	}
}

func (s *editorService) sendLSP(kind lspEventKind, path string) {
	var content string
	if kind != lspClose {
		var ok bool
		if content, ok = s.contentForPath(path); !ok {
			return // closed or renamed since; its own events cover that
		}
	}
	if s.lspSendHook != nil {
		s.lspSendHook(kind, path, content)
		return
	}
	if s.lspMgr == nil {
		return
	}
	switch kind {
	case lspOpen:
		s.lspMgr.DidOpen(path, content)
	case lspChange:
		s.lspMgr.DidChange(path, content)
	case lspClose:
		s.lspMgr.DidClose(path)
	}
}

// contentForPath returns the current content of the open buffer at path.
func (s *editorService) contentForPath(path string) (string, bool) {
	s.mu.Lock()
	for _, entry := range s.buffers {
		if buf := entry.buf; buf.Path() == path {
			s.mu.Unlock()
			return buf.Content(), true
		}
	}
	s.mu.Unlock()
	return "", false
}
