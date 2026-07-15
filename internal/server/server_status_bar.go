package server

import (
	"context"
	"sort"
	"sync"

	"github.com/indiejames/indigo/internal/plugin"
	proto "github.com/indiejames/indigo/internal/proto"
)

// statusBarEntry holds a text segment together with the connection that set it.
type statusBarEntry struct {
	connID uint64
	text   string
}

// statusBarRegistry stores named text segments contributed by any connected
// client. Entries are scoped to a connection; all entries for a connection are
// removed automatically when that connection closes.
type statusBarRegistry struct {
	mu      sync.RWMutex
	entries map[string]statusBarEntry // user key → entry
}

func newStatusBarRegistry() *statusBarRegistry {
	return &statusBarRegistry{entries: make(map[string]statusBarEntry)}
}

func (r *statusBarRegistry) set(connID uint64, key, text string) {
	r.mu.Lock()
	if text == "" {
		if e, ok := r.entries[key]; ok && (e.connID == connID || connID == 0) {
			delete(r.entries, key)
		}
	} else {
		r.entries[key] = statusBarEntry{connID: connID, text: text}
	}
	r.mu.Unlock()
}

// clearForConn removes all entries that were set by the given connection.
func (r *statusBarRegistry) clearForConn(connID uint64) {
	r.mu.Lock()
	for k, e := range r.entries {
		if e.connID == connID {
			delete(r.entries, k)
		}
	}
	r.mu.Unlock()
}

// asDecorations returns all entries as DecorationKindStatusBar decorations in
// a stable (sorted-by-key) order.
func (r *statusBarRegistry) asDecorations() []plugin.PluginDecoration {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.entries) == 0 {
		return nil
	}
	keys := make([]string, 0, len(r.entries))
	for k := range r.entries {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]plugin.PluginDecoration, 0, len(keys))
	for _, k := range keys {
		out = append(out, plugin.PluginDecoration{
			Kind: plugin.DecorationKindStatusBar,
			Text: r.entries[k].text,
		})
	}
	return out
}

// connSvc wraps *editorService and overrides SetStatusBarText to scope entries
// to a specific connection. All other methods delegate to the embedded service.
type connSvc struct {
	*editorService
	connID uint64
}

func (c *connSvc) SetStatusBarText(_ context.Context, call proto.EditorService_setStatusBarText) error {
	args := call.Args()
	key, err := args.Key()
	if err != nil {
		return err
	}
	text, err := args.Text()
	if err != nil {
		return err
	}
	c.statusBar.set(c.connID, key, text)
	_, err = call.AllocResults()
	return err
}

// SetStatusBarText on *editorService satisfies the interface but is unreachable
// in practice because all real connections go through connSvc. It falls back to
// connID 0 (no auto-cleanup on disconnect).
func (s *editorService) SetStatusBarText(_ context.Context, call proto.EditorService_setStatusBarText) error {
	args := call.Args()
	key, err := args.Key()
	if err != nil {
		return err
	}
	text, err := args.Text()
	if err != nil {
		return err
	}
	s.statusBar.set(0, key, text)
	_, err = call.AllocResults()
	return err
}
