package lsp

import (
	"encoding/json"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeDocServer is a language server that keeps the full text of one document
// from didOpen/didChange, and formats by upper-casing whatever it holds — so
// its edits are computed against its own copy, as a real server's are.
type fakeDocServer struct {
	mu    sync.Mutex
	text  string
	texts chan string // every text it is told, in order
	conn  *jsonrpcConn
}

func newFakeDocServer(t *testing.T, end net.Conn) *fakeDocServer {
	f := &fakeDocServer{texts: make(chan string, 64)}
	f.conn = newJSONRPCConn(end, end,
		func(method string, params json.RawMessage) {
			var text string
			switch method {
			case "textDocument/didOpen":
				var p DidOpenTextDocumentParams
				json.Unmarshal(params, &p) //nolint:errcheck
				text = p.TextDocument.Text
			case "textDocument/didChange":
				var p DidChangeTextDocumentParams
				json.Unmarshal(params, &p) //nolint:errcheck
				text = p.ContentChanges[0].Text
				// What a real server does after a change, and what used to
				// wedge RenameAfterChange: the publish is handled on the
				// client's read loop. Sent from its own goroutine, since
				// net.Pipe writes block until read.
				uri, ver := p.TextDocument.URI, p.TextDocument.Version
				go f.conn.Notify("textDocument/publishDiagnostics", PublishDiagnosticsParams{URI: uri, Version: &ver}) //nolint:errcheck
			default:
				return
			}
			f.mu.Lock()
			f.text = text
			f.mu.Unlock()
			f.texts <- text
		},
		func(method string, _ json.RawMessage) (any, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			switch method {
			case "textDocument/formatting":
				lines := strings.Split(f.text, "\n")
				end := Position{Line: len(lines) - 1, Character: len(lines[len(lines)-1])}
				return []TextEdit{{Range: Range{End: end}, NewText: strings.ToUpper(f.text)}}, nil
			case "textDocument/rename":
				return WorkspaceEdit{}, nil
			}
			return nil, nil
		},
	)
	return f
}

// waitText waits until the server has been told want, last.
func (f *fakeDocServer) waitText(t *testing.T, want string) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		f.mu.Lock()
		got := f.text
		f.mu.Unlock()
		if got == want && len(f.texts) == 0 {
			return
		}
		select {
		case <-f.texts:
		case <-deadline:
			t.Fatalf("server holds %q, want %q", got, want)
		}
	}
}

func pipeClient(t *testing.T, caps ServerCapabilities) (*Client, *fakeDocServer) {
	clientEnd, serverEnd := net.Pipe()
	t.Cleanup(func() {
		clientEnd.Close() //nolint:errcheck
		serverEnd.Close() //nolint:errcheck
	})
	f := newFakeDocServer(t, serverEnd)
	c := &Client{
		docVersions: make(map[string]int),
		diagnostics: make(map[string][]Diagnostic),
		caps:        caps,
	}
	c.conn = newJSONRPCConn(clientEnd, clientEnd, c.handleNotification, nil)
	return c, f
}

// TestFormatFormatsTheContentItIsGiven: a server computes formatting edits
// against its own copy of the document, and Format applied them to the
// content its caller passed. Those differ whenever an edit is still on its way
// to the server, and the edits then landed at offsets that meant something
// else. Format now tells the server the content first, and afterwards puts
// back what it held, so the next edit's didChange is still the right diff.
func TestFormatFormatsTheContentItIsGiven(t *testing.T) {
	c, f := pipeClient(t, ServerCapabilities{DocumentFormattingProvider: true})
	if err := c.DidOpen("/a.go", "older text\nsecond line\n"); err != nil {
		t.Fatal(err)
	}
	f.waitText(t, "older text\nsecond line\n")

	got, changed, err := c.Format("/a.go", "new\n", FormattingOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !changed || got != "NEW\n" {
		t.Fatalf("Format = %q, %v; want %q formatted from the content it was given", got, changed, "NEW\n")
	}
	f.waitText(t, "older text\nsecond line\n")
}

// TestRenameAfterChangeDoesNotStallOnDiagnostics: RenameAfterChange held the
// client's main lock across the rename round trip. Its didChange makes the
// server publish diagnostics, whose handler takes that same lock on the read
// loop — so the read loop waited for the rename, the rename waited for a
// response only the read loop could read, and it ended only at the timeout.
func TestRenameAfterChangeDoesNotStallOnDiagnostics(t *testing.T) {
	c, _ := pipeClient(t, ServerCapabilities{RenameProvider: true})
	if err := c.DidOpen("/a.go", "x\n"); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := c.RenameAfterChange("/a.go", "y\n", 0, 0, "z"); err != nil {
		t.Fatalf("RenameAfterChange: %v", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("RenameAfterChange took %v: stalled behind the diagnostics handler", d)
	}
}
