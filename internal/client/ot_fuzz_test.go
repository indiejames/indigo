package client

import (
	"context"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/indiejames/indigo/internal/config"
	"github.com/indiejames/indigo/internal/document"
	"github.com/indiejames/indigo/internal/server"
)

// This is the harness the whole operational-transform effort is verified by.
//
// Everything before it is checked against interleavings somebody thought of:
// the transform has its own property tests, and the server and client halves
// have hand-built cases. Convergence is a property of the two halves running
// against each other under interleavings nobody thought of, which is what this
// exercises — a real server over a real socket, real client Models, real
// send queues, random edits, random poll timing.
//
// The oracle is the invariant OT exists to provide, and the only one available:
// once every message has been delivered, every participant holds identical
// content. There is no expected document to compare against, because the result
// legitimately depends on the interleaving the fuzzer happened to pick.

// otClient is one simulated editor window: a real client.Model with its own
// connection, driven directly rather than through Bubble Tea's event loop.
type otClient struct {
	m   Model
	rpc *RPC
	// drain holds the command that will flush this client's send queue. It is
	// kept rather than run immediately so the fuzz can poll while edits are
	// still in flight — the only state in which the *client* half of the
	// transform does any work. Running it eagerly hides that half completely:
	// with nothing unacknowledged, rebasePastPending has nothing to rebase
	// against, and disabling it entirely still converges.
	drain func() any
	name  string
}

// runCmd executes cmd (if any) and feeds its message back into the Model.
func (c *otClient) runCmd(t *testing.T, cmd func() any) {
	t.Helper()
	if cmd == nil {
		return
	}
	if msg := cmd(); msg != nil {
		c.integrate(t, msg)
	}
}

// integrate folds one message into the Model and asserts that no resync was
// provoked.
//
// This is the property the resync fallback was narrowed to, checked directly
// rather than inferred: after operational transform, ordinary concurrent
// editing converges on its own, so a resync should only ever follow a wholesale
// buffer swap or a genuine RPC failure — and this fuzz does neither. Without
// this assertion the harness would happily pass while every other keystroke
// silently round-tripped the whole buffer.
func (c *otClient) integrate(t *testing.T, msg any) {
	t.Helper()
	if f, ok := msg.(applyOpFailedMsg); ok {
		t.Fatalf("%s: an edit failed to reach the server (%v); ordinary concurrent editing must not fail",
			c.name, f.err)
	}
	updated, _ := c.m.Update(msg)
	c.m = updated.(Model)
	if strings.Contains(strings.ToLower(c.m.status), "resync") {
		t.Fatalf("%s: a resync was triggered during ordinary concurrent editing: %q", c.name, c.m.status)
	}
}

// edit applies an op locally and queues it for the server, without flushing —
// the real path an edit takes, minus the highlighting and undo bookkeeping that
// convergence does not depend on.
func (c *otClient) edit(t *testing.T, op document.Op) {
	t.Helper()
	m, teaCmd := c.m.sendOp(op)
	c.m = m
	if teaCmd != nil && c.drain == nil {
		// Only the first enqueue since the last flush yields a command; later
		// ones join the drain already pending, exactly as in the editor.
		c.drain = func() any { return teaCmd() }
	}
}

// flush runs the pending drain, sending everything queued.
func (c *otClient) flush(t *testing.T) {
	t.Helper()
	if c.drain == nil {
		return
	}
	cmd := c.drain
	c.drain = nil
	c.runCmd(t, cmd)
}

// poll fetches updates once and integrates them, mirroring the client's rule
// that a poll only goes out when nothing is waiting to be sent.
func (c *otClient) poll(t *testing.T) {
	t.Helper()
	if !c.m.sendQ.idle() {
		c.flush(t)
	}
	fetch := c.m.fetchUpdates()
	c.runCmd(t, func() any { return fetch() })
}

// pollWithEditInFlight models the window the client-side rebase exists for: the
// poll's round trip has completed but its result has not been integrated yet,
// and the user types in between. Those ops are in pending when the response is
// processed, so the incoming ops must be rebased past them.
//
// Without this the harness cannot exercise the client half at all — with the
// poll gate in place, a plain poll never has anything outstanding to rebase
// against, and removing the client-side rebase entirely still converges.
func (c *otClient) pollWithEditInFlight(t *testing.T, op document.Op) {
	t.Helper()
	if !c.m.sendQ.idle() {
		c.flush(t)
	}
	fetch := c.m.fetchUpdates()
	msg := fetch() // round trip done; not yet integrated
	c.edit(t, op)  // typed in the window
	if msg != nil {
		c.integrate(t, msg)
	}
}

// settle polls repeatedly until nothing changes, so every participant has
// integrated everything before the oracle runs.
func (c *otClient) settle(t *testing.T) {
	t.Helper()
	for i := 0; i < 16; i++ {
		before := c.m.buf.Content()
		beforeVer := c.m.version
		c.flush(t)
		c.poll(t)
		if c.m.buf.Content() == before && c.m.version == beforeVer && c.drain == nil {
			return
		}
	}
}

// startOTServer runs a real server in-process and returns its socket path.
func startOTServer(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("INDIGO_LOG_DIR", t.TempDir())
	srv, err := server.New(dir)
	if err != nil {
		t.Skipf("cannot start a server here: %v", err)
	}
	t.Cleanup(srv.Wait)
	return dir, server.SocketPath(dir)
}

func dialOTClient(t *testing.T, sock, path, workDir, name string) *otClient {
	t.Helper()
	r, err := Dial(sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		r.Disconnect(ctx) //nolint:errcheck
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	bufID, content, version, fromRecovery, generation, err := r.OpenFile(ctx, path)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	m := New(r, bufID, content, version, path, workDir, &config.Config{}, fromRecovery, generation)
	return &otClient{m: m, rpc: r, name: name}
}

// randomEditFor builds an op valid against this client's current buffer.
func randomEditFor(rnd *rand.Rand, m Model) document.Op {
	lines := m.buf.LineCount()
	line := rnd.Intn(lines)
	col := rnd.Intn(m.buf.LineLen(line) + 1)
	if rnd.Intn(3) == 0 {
		// Delete a short span on one line, when there is anything to delete.
		end := col + 1 + rnd.Intn(3)
		if max := m.buf.LineLen(line); end > max {
			end = max
		}
		if end > col {
			return document.Op{
				ClientID: m.rpc.ClientID(), Type: document.OpDelete,
				FromLine: line, FromCol: col, ToLine: line, ToCol: end,
			}
		}
	}
	texts := []string{"a", "bc", "\n", "x\ny", "é", "🙂"}
	return document.Op{
		ClientID: m.rpc.ClientID(), Type: document.OpInsert,
		InsertLine: line, InsertCol: col, InsertText: texts[rnd.Intn(len(texts))],
	}
}

// TestOTConvergenceFuzz is the end-to-end convergence check.
func TestOTConvergenceFuzz(t *testing.T) {
	if testing.Short() {
		t.Skip("fuzz harness drives real sockets; skipped in -short")
	}
	const (
		scenarios = 60
		rounds    = 30
	)
	rnd := rand.New(rand.NewSource(20260915))

	for sc := 0; sc < scenarios; sc++ {
		func() {
			dir, sock := startOTServer(t)
			path := filepath.Join(dir, "shared.txt")
			if err := os.WriteFile(path, []byte("hello world\nsecond line\n"), 0o644); err != nil {
				t.Fatal(err)
			}

			a := dialOTClient(t, sock, path, dir, "A")
			b := dialOTClient(t, sock, path, dir, "B")
			clients := []*otClient{a, b}

			for r := 0; r < rounds; r++ {
				c := clients[rnd.Intn(len(clients))]
				switch rnd.Intn(6) {
				case 0, 1, 2:
					c.edit(t, randomEditFor(rnd, c.m))
				case 3:
					c.flush(t)
				case 4:
					c.pollWithEditInFlight(t, randomEditFor(rnd, c.m))
				default:
					// Polling at random moments is the point: it decides how
					// much each client has integrated when its next edit is
					// computed, and — crucially — lets a poll land while this
					// client still has edits in flight, which is the only state
					// in which the client half of the transform matters.
					c.poll(t)
				}
			}

			// Quiesce: everything sent, everything delivered.
			for i := 0; i < 4; i++ {
				for _, c := range clients {
					c.settle(t)
				}
			}

			want := a.m.buf.Content()
			for _, c := range clients[1:] {
				if got := c.m.buf.Content(); got != want {
					t.Fatalf("scenario %d: clients diverged\n  %s: %q\n  %s: %q",
						sc, a.name, want, c.name, got)
				}
			}

			// And against the server, which is what a save would write.
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			serverContent, _, _, _, err := a.rpc.GetBufferSnapshot(ctx, a.m.bufID)
			cancel()
			if err != nil {
				t.Fatalf("GetBufferSnapshot: %v", err)
			}
			if serverContent != want {
				t.Fatalf("scenario %d: clients agree on %q but the server holds %q",
					sc, want, serverContent)
			}
		}()
	}
}
