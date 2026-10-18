// Package plugintest runs a real plugin binary under a real plugin.Manager, so
// a bundled plugin can be tested from its own directory the way the editor runs
// it: built from source, installed with its shipped plugin.toml, spawned, and
// spoken to over the plugin socket. Only the server behind the Manager is fake.
//
// The shipped manifest is used on purpose. A menu_item whose command names an id
// the code never registers, or a [binaries] entry that does not match the build,
// fails here rather than in someone's editor.
package plugintest

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/indiejames/indigo/internal/plugin"
)

// Start builds the plugin whose source is in srcDir (normally "."), installs it
// with srcDir's plugin.toml, and starts a Manager serving it against bridge.
// The Manager is shut down when the test ends.
func Start(t *testing.T, srcDir string, bridge plugin.ServerBridge) *plugin.Manager {
	t.Helper()
	srcDir, err := filepath.Abs(srcDir)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := os.ReadFile(filepath.Join(srcDir, "plugin.toml"))
	if err != nil {
		t.Fatalf("read plugin.toml: %v", err)
	}
	name := filepath.Base(srcDir)
	pluginsDir := t.TempDir()
	installDir := filepath.Join(pluginsDir, name)
	if err := os.MkdirAll(installDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(installDir, "plugin.toml"), manifest, 0o644); err != nil {
		t.Fatal(err)
	}
	// The name every bundled manifest gives this platform's binary.
	bin := filepath.Join(installDir, name+"-"+runtime.GOOS+"-"+runtime.GOARCH)
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = srcDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", name, err, out)
	}
	t.Setenv("INDIGO_PLUGINS_DIR", pluginsDir)
	t.Setenv("INDIGO_LOG_DIR", t.TempDir())

	m := plugin.NewManager(t.TempDir(), bridge)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := m.Start(ctx); err != nil {
		t.Fatalf("start %s: %v", name, err)
	}
	t.Cleanup(m.Shutdown)
	return m
}

// Message is one status-bar message a plugin showed.
type Message struct {
	ClientID uint64 // 0 means every client
	Text     string
}

// Bridge is a ServerBridge holding buffers as lines, a visible range per client,
// and a record of the messages and registrations a plugin made. The embedded
// nil interface makes any method it does not model panic, so a test cannot
// drift into exercising something it does not describe.
type Bridge struct {
	plugin.ServerBridge

	mu       sync.Mutex
	buffers  map[uint32][]string
	visible  map[uint64][2]uint32
	messages []Message
	keys     []string
	changed  chan struct{}
}

// NewBridge returns an empty Bridge.
func NewBridge() *Bridge {
	return &Bridge{
		buffers: map[uint32][]string{},
		visible: map[uint64][2]uint32{},
		changed: make(chan struct{}, 1),
	}
}

// SetBuffer gives bufID these lines.
func (b *Bridge) SetBuffer(bufID uint32, lines []string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buffers[bufID] = lines
}

// SetVisible makes lines start..end (inclusive) visible to clientID.
func (b *Bridge) SetVisible(clientID uint64, start, end uint32) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.visible[clientID] = [2]uint32{start, end}
}

// Messages returns the messages shown so far.
func (b *Bridge) Messages() []Message {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]Message(nil), b.messages...)
}

// WaitForMessage waits until a message with text has been shown and returns it.
// A plugin's message is sent over its own connection, so it can arrive after
// the call that caused it returns.
func (b *Bridge) WaitForMessage(t *testing.T, text string) Message {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		for _, m := range b.Messages() {
			if m.Text == text {
				return m
			}
		}
		select {
		case <-b.changed:
		case <-deadline:
			t.Fatalf("no message %q was shown; got %+v", text, b.Messages())
		}
	}
}

// Keys returns the key triggers registered so far.
func (b *Bridge) Keys() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.keys...)
}

func (b *Bridge) notify() {
	select {
	case b.changed <- struct{}{}:
	default:
	}
}

func (b *Bridge) PluginShowMessage(clientID uint64, text string) {
	b.mu.Lock()
	b.messages = append(b.messages, Message{ClientID: clientID, Text: text})
	b.mu.Unlock()
	b.notify()
}

func (b *Bridge) PluginKeyRegistered(trigger string) {
	b.mu.Lock()
	b.keys = append(b.keys, trigger)
	b.mu.Unlock()
}

func (b *Bridge) PluginInsertHookRegistered(string)           {}
func (b *Bridge) PluginDecorationsChanged(uint32)             {}
func (b *Bridge) PluginOpenBuffers() []plugin.PluginBufferRef { return nil }

func (b *Bridge) PluginVisibleRange(clientID uint64) (uint32, uint32, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	r, ok := b.visible[clientID]
	if !ok {
		return 0, 0, os.ErrNotExist
	}
	return r[0], r[1], nil
}

// PluginReadLines returns lines [start, end), clamped to the buffer, as the
// server does.
func (b *Bridge) PluginReadLines(bufID uint32, start, end uint32) ([]string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	lines, ok := b.buffers[bufID]
	if !ok {
		return nil, os.ErrNotExist
	}
	n := uint32(len(lines))
	start, end = min(start, n), min(end, n)
	if start > end {
		start = end
	}
	return append([]string(nil), lines[start:end]...), nil
}
