package agenttools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/indiejames/indigo/internal/rpcclient"
	"github.com/indiejames/indigo/internal/server"
)

// An agent that read a CRLF file with its own file tools quotes "\r\n" back in
// old_text and new_text. apply_edits must still find the text, keep "\r" out of
// the buffer, and the save must write single "\r\n"s — never "\r\r\n".
func TestApplyEditsOnACRLFFileWithCRLFText(t *testing.T) {
	t.Setenv("INDIGO_LOG_DIR", t.TempDir())
	t.Setenv("INDIGO_PLUGINS_DIR", t.TempDir())
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

	path := filepath.Join(dir, "win.txt")
	if err := os.WriteFile(path, []byte("one\r\ntwo\r\nthree\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	in, _ := json.Marshal(map[string]string{
		"path": path, "reason": "test",
		"old_text": "one\r\ntwo\r\n", "new_text": "ONE\r\nTWO\r\nextra\r\n",
	})
	if out, isErr := ExecTool(ctx, r, AlwaysApprove{}, dir, "apply_edits", in); isErr {
		t.Fatalf("apply_edits: %s", out)
	}
	if out, isErr := ExecTool(ctx, r, AlwaysApprove{}, dir, "save_file", json.RawMessage(`{"path":"`+path+`"}`)); isErr {
		t.Fatalf("save_file: %s", out)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := "ONE\r\nTWO\r\nextra\r\nthree\r\n"; string(got) != want {
		t.Errorf("saved %q, want %q", got, want)
	}
}
