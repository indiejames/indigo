package app

import (
	"context"
	"crypto/sha256"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/indiejames/indigo/internal/client"
	"github.com/indiejames/indigo/internal/config"
	"github.com/indiejames/indigo/internal/server"
)

// typeKeys presses each key in turn and runs what it starts, as the runtime
// would.
func typeKeys(t *testing.T, a App, keys ...tea.KeyPressMsg) App {
	t.Helper()
	for _, k := range keys {
		updated, cmd := a.Update(k)
		a = run(t, updated.(App), cmd, time.Second, 0)
	}
	return a
}

func char(r rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: r, Text: string(r)} }

var esc = tea.KeyPressMsg{Code: tea.KeyEscape}

func bufferFor(t *testing.T, a App, path string) client.Model {
	t.Helper()
	for _, b := range a.buffers {
		if b.FilePath() == path {
			return b
		}
	}
	t.Fatalf("no buffer for %s", path)
	return client.Model{}
}

func shows(m client.Model, want string) bool {
	sum := sha256.Sum256([]byte(want))
	return string(m.BufferStateFor().ContentSha256) == string(sum[:])
}

// TestAWindowSurvivesItsConnectionDropping drives the dev-container case end to
// end against real servers: the stream to the server breaks (as a `docker exec`
// session does when the Mac sleeps), the server exits because its only client
// has gone, and the window reconnects to the server its redial starts.
//
// Two buffers check the two ways a buffer is reconciled. b.txt had an edit on
// its way to the server when the connection broke, which the new server
// cannot have, so the window's text wins and is sent to it. a.txt had no
// unsaved changes and its file changed on disk during the outage, so it is
// reloaded. Typing afterwards checks that the window really is talking to the
// new server about the right buffers.
func TestAWindowSurvivesItsConnectionDropping(t *testing.T) {
	t.Setenv("INDIGO_LOG_DIR", t.TempDir())
	t.Setenv("INDIGO_PLUGINS_DIR", t.TempDir())
	t.Setenv("HOME", t.TempDir()) // recovery files
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	aPath, bPath := filepath.Join(dir, "a.txt"), filepath.Join(dir, "b.txt")
	os.WriteFile(aPath, []byte("alpha\n"), 0o644) //nolint:errcheck
	os.WriteFile(bPath, []byte("bravo\n"), 0o644) //nolint:errcheck

	srv1, err := server.New(dir)
	if err != nil {
		t.Skipf("cannot start a server: %v", err)
	}
	conn1, err := net.Dial("unix", server.SocketPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	rpc, err := client.DialStream(conn1)
	if err != nil {
		t.Fatal(err)
	}

	// The redial is what a dev container window's does, minus docker: wait for
	// the old server to go (in the container it exits by itself), start
	// another, and dial it. The file changes on disk while neither is running.
	var (
		once     sync.Once
		srv2     *server.Server
		startErr error
	)
	t.Cleanup(func() {
		if srv2 != nil {
			srv2.Wait()
		} else {
			srv1.Wait()
		}
	})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		rpc.Disconnect(ctx) //nolint:errcheck
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	bufID, content, version, _, generation, err := rpc.OpenFile(ctx, aPath)
	if err != nil {
		t.Fatal(err)
	}
	a := *New(rpc, bufID, content, version, aPath, &config.Config{}, false, dir, 0, generation)
	q := &pushQueue{}
	rpc.SetPushSender(q.send)
	rpc.SetRedial(func(ctx context.Context) (io.ReadWriteCloser, string, error) {
		once.Do(func() {
			srv1.Wait()
			os.WriteFile(aPath, []byte("changed on disk\n"), 0o644) //nolint:errcheck
			srv2, startErr = server.New(dir)
		})
		if startErr != nil {
			return nil, "", startErr
		}
		c, err := net.Dial("unix", server.SocketPath(dir))
		return c, "", err
	})

	updated, cmd := a.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	a = run(t, updated.(App), cmd, time.Second, 0)
	updated, cmd = a.Update(client.OpenFileAtMsg{Path: bPath, Col: -1})
	a = run(t, updated.(App), cmd, 3*time.Second, 0)
	a = typeKeys(t, a, char('i'), char('X'), esc) // reaches the first server

	conn1.Close() //nolint:errcheck
	// Typed before the window has heard of the drop: this edit is sent on a
	// dead connection and never reaches any server.
	a = typeKeys(t, a, char('A'), char('Y'), esc)

	a = pump(t, a, q, "the reconnect to finish", func(a App) bool {
		return a.reconnectSeq == 1 && !a.reconnecting
	})

	const bWant = "XbravoY\n"
	if b := bufferFor(t, a, bPath); !shows(b, bWant) {
		t.Errorf("b.txt's window no longer shows %q", bWant)
	}
	checkServer := func(path, want string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		got, _, _, _, err := rpc.GetBufferSnapshot(ctx, bufferFor(t, a, path).BufID())
		if err != nil {
			t.Fatalf("snapshot of %s: %v", filepath.Base(path), err)
		}
		if got != want {
			t.Errorf("the new server has %s as %q, want %q", filepath.Base(path), got, want)
		}
	}
	checkServer(bPath, bWant)

	const aWant = "changed on disk\n"
	if !shows(bufferFor(t, a, aPath), aWant) {
		t.Errorf("a.txt, clean and changed on disk, was not reloaded to %q", aWant)
	}
	checkServer(aPath, aWant)

	a = typeKeys(t, a, char('A'), char('Z'), esc)
	checkServer(bPath, "XbravoYZ\n")
}

// TestInputIsRefusedWhileReconnecting: nothing typed during an outage could
// reach a server, and an edit made before the buffers are reopened would have
// nothing to be reconciled against. ctrl+c still quits.
func TestInputIsRefusedWhileReconnecting(t *testing.T) {
	a := App{cfg: &config.Config{}, width: 100, height: 24, fileChangedIdx: -1,
		buffers: []client.Model{newReloadTestModel(1, "/x/a.go")}}
	before := a.buffers[0].BufferStateFor().ContentSha256
	a = a.handleReconnecting()

	a, cmd := step(t, a, char('i'))
	a, _ = step(t, a, char('x'))
	if cmd != nil || string(a.buffers[0].BufferStateFor().ContentSha256) != string(before) {
		t.Error("a key reached the buffer while reconnecting")
	}
	if _, cmd = step(t, a, tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}); cmd == nil {
		t.Fatal("ctrl+c did nothing while reconnecting")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("ctrl+c while reconnecting did not quit")
	}
}
