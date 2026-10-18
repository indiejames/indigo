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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/indiejames/indigo/internal/plugin"
)

// Start builds the plugin whose source is in srcDir (normally "."), installs it
// with srcDir's plugin.toml, and starts a Manager serving it against bridge.
// The Manager is shut down when the test ends.
//
// The plugin runs with HOME set to an empty temporary directory, so one that
// keeps state under the user's home (bookmarks does) cannot touch the real one.
// The returned string is that directory.
func Start(t *testing.T, srcDir string, bridge plugin.ServerBridge) (*plugin.Manager, string) {
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
	// After the build: a new HOME before it would start Go's build cache cold.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("INDIGO_PLUGINS_DIR", pluginsDir)
	t.Setenv("INDIGO_LOG_DIR", t.TempDir())

	m := plugin.NewManager(t.TempDir(), bridge)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := m.Start(ctx); err != nil {
		t.Fatalf("start %s: %v", name, err)
	}
	t.Cleanup(m.Shutdown)
	return m, home
}

// Eventually waits up to five seconds for cond, failing with msg if it never
// holds. A plugin's calls back into the editor travel over its own connection,
// so their effects can land after the call that caused them returns.
func Eventually(t *testing.T, msg string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal(msg)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Message is one status-bar message a plugin showed.
type Message struct {
	ClientID uint64 // 0 means every client
	Text     string
}

// Popup is a list popup a plugin showed, answerable with Select or Cancel.
type Popup struct {
	Title    string
	Items    []plugin.PluginPopupItem
	onSelect func(string)
	onCancel func()
}

// Select answers the popup as if the user picked the item carrying data.
func (p Popup) Select(data string) { p.onSelect(data) }

// Prompt is a text-input prompt a plugin showed, answerable with Confirm.
type Prompt struct {
	Title     string
	onConfirm func(string)
}

// Confirm answers the prompt as if the user typed text and pressed Enter.
func (p Prompt) Confirm(text string) { p.onConfirm(text) }

// Opened is one OpenFile request.
type Opened struct {
	Path string
	Line uint32
}

type buffer struct {
	path    string
	content string
}

// Bridge is a ServerBridge holding buffers, a visible range per client, and a
// record of what a plugin asked the editor to do. The embedded nil interface
// makes any method it does not model panic, so a test cannot drift into
// exercising something it does not describe.
type Bridge struct {
	plugin.ServerBridge

	mu       sync.Mutex
	buffers  map[uint32]buffer
	visible  map[uint64][2]uint32
	messages []Message
	keys     []string
	popups   []Popup
	prompts  []Prompt
	opened   []Opened
	refresh  map[uint32]int
}

// NewBridge returns an empty Bridge.
func NewBridge() *Bridge {
	return &Bridge{
		buffers: map[uint32]buffer{},
		visible: map[uint64][2]uint32{},
		refresh: map[uint32]int{},
	}
}

// SetBuffer gives bufID these lines and no path.
func (b *Bridge) SetBuffer(bufID uint32, lines []string) {
	b.OpenBuffer(bufID, "", strings.Join(lines, "\n"))
}

// OpenBuffer gives bufID a path and content. It does not tell the plugin; call
// the Manager's DispatchBufferOpen for that, as the server does.
func (b *Bridge) OpenBuffer(bufID uint32, path, content string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buffers[bufID] = buffer{path: path, content: content}
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
func (b *Bridge) WaitForMessage(t *testing.T, text string) Message {
	t.Helper()
	var found Message
	Eventually(t, "no message "+strconvQuote(text)+" was shown", func() bool {
		for _, m := range b.Messages() {
			if m.Text == text {
				found = m
				return true
			}
		}
		return false
	})
	return found
}

// Keys returns the key triggers registered so far.
func (b *Bridge) Keys() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.keys...)
}

// WaitForPopup waits for the nth popup (counting from 1) and returns it.
func (b *Bridge) WaitForPopup(t *testing.T, n int) Popup {
	t.Helper()
	var p Popup
	Eventually(t, "the plugin never showed a popup", func() bool {
		b.mu.Lock()
		defer b.mu.Unlock()
		if len(b.popups) < n {
			return false
		}
		p = b.popups[n-1]
		return true
	})
	return p
}

// WaitForPrompt waits for the nth input prompt (counting from 1).
func (b *Bridge) WaitForPrompt(t *testing.T, n int) Prompt {
	t.Helper()
	var p Prompt
	Eventually(t, "the plugin never showed an input prompt", func() bool {
		b.mu.Lock()
		defer b.mu.Unlock()
		if len(b.prompts) < n {
			return false
		}
		p = b.prompts[n-1]
		return true
	})
	return p
}

// Opened returns the OpenFile requests made so far.
func (b *Bridge) Opened() []Opened {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]Opened(nil), b.opened...)
}

// Refreshes reports how many times the plugin pushed a decoration refresh for
// bufID.
func (b *Bridge) Refreshes(bufID uint32) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.refresh[bufID]
}

func (b *Bridge) PluginShowMessage(clientID uint64, text string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.messages = append(b.messages, Message{ClientID: clientID, Text: text})
}

func (b *Bridge) PluginKeyRegistered(trigger string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.keys = append(b.keys, trigger)
}

func (b *Bridge) PluginShowPopup(title string, items []plugin.PluginPopupItem, onSelect func(string), onCancel func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.popups = append(b.popups, Popup{Title: title, Items: items, onSelect: onSelect, onCancel: onCancel})
}

func (b *Bridge) PluginShowInputPrompt(title, _ string, onConfirm func(string), _ func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.prompts = append(b.prompts, Prompt{Title: title, onConfirm: onConfirm})
}

func (b *Bridge) PluginOpenFile(path string, line uint32) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.opened = append(b.opened, Opened{Path: path, Line: line})
	return nil
}

func (b *Bridge) PluginDecorationsChanged(bufID uint32) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refresh[bufID]++
}

func (b *Bridge) PluginInsertHookRegistered(string)           {}
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

func (b *Bridge) buffer(bufID uint32) (buffer, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	buf, ok := b.buffers[bufID]
	if !ok {
		return buffer{}, os.ErrNotExist
	}
	return buf, nil
}

func (b *Bridge) PluginReadBuffer(bufID uint32) (string, uint64, uint64, error) {
	buf, err := b.buffer(bufID)
	return buf.content, 0, 0, err
}

// PluginReadLines returns lines [start, end), clamped to the buffer, as the
// server does.
func (b *Bridge) PluginReadLines(bufID uint32, start, end uint32) ([]string, error) {
	buf, err := b.buffer(bufID)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(buf.content, "\n")
	n := uint32(len(lines))
	start, end = min(start, n), min(end, n)
	if start > end {
		start = end
	}
	return lines[start:end], nil
}

func (b *Bridge) PluginBufferInfo(bufID uint32) (string, string, uint32, bool, uint64, error) {
	buf, err := b.buffer(bufID)
	return buf.path, "", uint32(strings.Count(buf.content, "\n") + 1), false, 0, err
}

// PluginRunProcess runs the command on this machine, as the server does.
func (b *Bridge) PluginRunProcess(name string, args []string) (string, string, int32, error) {
	cmd := exec.Command(name, args...)
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if exitErr, ok := err.(*exec.ExitError); ok {
		return stdout.String(), stderr.String(), int32(exitErr.ExitCode()), nil
	}
	return stdout.String(), stderr.String(), 0, err
}

func strconvQuote(s string) string { return `"` + s + `"` }
