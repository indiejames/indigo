//go:build indigo_debug

package agenttools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/indiejames/indigo/internal/debuglog"
	"github.com/indiejames/indigo/internal/faultinject"
	"github.com/indiejames/indigo/internal/rpcclient"
	"github.com/indiejames/indigo/internal/server"
	"github.com/indiejames/indigo/internal/syncevent"
)

// editLostHarness starts a real server and returns a connection to it, the
// workspace, and a file holding "alpha\nbeta\n".
func editLostHarness(t *testing.T) (*rpcclient.RPC, string, string) {
	t.Helper()
	t.Setenv("INDIGO_LOG_DIR", t.TempDir())
	t.Setenv("INDIGO_PLUGINS_DIR", t.TempDir())
	t.Cleanup(faultinject.Reset)
	dir := t.TempDir()
	srv, err := server.New(dir)
	if err != nil {
		t.Skipf("cannot start a server here: %v", err)
	}
	t.Cleanup(srv.Wait)
	r, err := rpcclient.Dial(server.SocketPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		r.Disconnect(ctx) //nolint:errcheck
	})
	path := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return r, dir, path
}

func editLostEvents(t *testing.T) []syncevent.Event {
	t.Helper()
	entries, err := debuglog.Read(debuglog.ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var out []syncevent.Event
	for _, e := range entries {
		if ev, ok := syncevent.Parse(e); ok && ev.Kind == syncevent.EditLost {
			out = append(out, ev)
		}
	}
	return out
}

// TestApplyEditsRecoversFromAnEditTheServerSwallowed reproduces the reported
// failure — the server accepts the batch and applies nothing — and checks the
// tool notices at the buffer, records it, and lands the edit on a retry.
func TestApplyEditsRecoversFromAnEditTheServerSwallowed(t *testing.T) {
	r, dir, path := editLostHarness(t)
	faultinject.SwallowNextApplyOps(1)

	in, _ := json.Marshal(map[string]string{"path": path, "reason": "t", "old_text": "beta", "new_text": "gamma"})
	out, isErr := ExecTool(context.Background(), r, AlwaysApprove{}, dir, "apply_edits", in)
	if isErr || !strings.Contains(out, "Applied on a retry") {
		t.Fatalf("apply_edits = %q (error %v), want success on a retry", out, isErr)
	}
	if got, _ := os.ReadFile(path); string(got) != "alpha\ngamma\n" {
		t.Errorf("file = %q, want the edit on disk exactly once", got)
	}
	evs := editLostEvents(t)
	if len(evs) != 1 || !strings.Contains(evs[0].Detail, "from the buffer") {
		t.Errorf("edit_lost events = %+v, want one naming the buffer as the stage", evs)
	}
	if len(evs) == 1 && strings.Contains(evs[0].Detail, "beta") {
		t.Errorf("an edit_lost event carried buffer text: %q", evs[0].Detail)
	}
}

// TestApplyEditsReportsAnEditLostTwice: the retry is one retry, and a second
// loss is an error rather than a loop or a false success.
func TestApplyEditsReportsAnEditLostTwice(t *testing.T) {
	r, dir, path := editLostHarness(t)
	faultinject.SwallowNextApplyOps(2)

	in, _ := json.Marshal(map[string]string{"path": path, "reason": "t", "old_text": "beta", "new_text": "gamma"})
	out, isErr := ExecTool(context.Background(), r, AlwaysApprove{}, dir, "apply_edits", in)
	if !isErr || !strings.Contains(out, "did NOT land") {
		t.Fatalf("apply_edits = %q (error %v), want an error saying the edit did not land", out, isErr)
	}
	if got, _ := os.ReadFile(path); string(got) != "alpha\nbeta\n" {
		t.Errorf("file = %q, want it untouched", got)
	}
	if n := len(editLostEvents(t)); n != 2 {
		t.Errorf("%d edit_lost events, want 2 (the loss and the retry's)", n)
	}
}
