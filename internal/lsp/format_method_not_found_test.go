package lsp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
)

// TestFormatTreatsMethodNotFoundAsUnsupported is a regression test: a
// server can advertise documentFormattingProvider in its initialize
// response capabilities but still return "Method not found" (-32601) on
// an actual textDocument/formatting request — observed with Godot's
// GDScript language server. Before this fix that error surfaced as a real
// error, popping an "ERR: ..." message on every save with format_on_save
// enabled. Client.Format must instead treat it exactly like "no formatting
// support" (content unchanged, no error), matching the existing
// null-result and no-capability cases handled just above it.
func TestFormatTreatsMethodNotFoundAsUnsupported(t *testing.T) {
	clientEnd, serverEnd := net.Pipe()
	defer clientEnd.Close() //nolint:errcheck
	defer serverEnd.Close() //nolint:errcheck

	go fakeFormattingServer(t, serverEnd)

	c := &Client{docVersions: make(map[string]int), rootURI: pathToURI("/workspace")}
	c.conn = newJSONRPCConn(clientEnd, clientEnd, nil, nil)

	if err := c.Initialize(); err != nil {
		t.Fatalf("Initialize() returned an error: %v", err)
	}

	const original = "func _ready():\n\tpass\n"
	content, changed, err := c.Format("/workspace/main.gd", original, FormattingOptions{TabSize: 4, InsertSpaces: true})
	if err != nil {
		t.Fatalf("Format() returned an error, want the -32601 to be swallowed: %v", err)
	}
	if changed {
		t.Fatal("Format() reported changed=true from a -32601 response")
	}
	if content != original {
		t.Fatalf("Format() content = %q, want the original content unchanged", content)
	}
}

// fakeFormattingServer answers exactly the two requests
// TestFormatTreatsMethodNotFoundAsUnsupported drives: initialize (with
// documentFormattingProvider advertised) and textDocument/formatting
// (answered with a -32601 error). This deliberately bypasses
// newJSONRPCConn's own reqHandler path on the server side — that path
// always maps a handler error to a fixed -32603 (see jsonrpcConn's
// server-to-client request dispatch), and this test needs precise control
// over the returned error code, so it speaks the Content-Length framing
// directly instead.
func fakeFormattingServer(t *testing.T, conn net.Conn) {
	t.Helper()
	br := bufio.NewReader(conn)
	// Loop until the formatting request has been answered, rather than a
	// fixed message count: Client.Initialize sends an "initialized"
	// notification (no id, no response expected) right after the
	// initialize request/response round trip, which this server must skip
	// over rather than treat as the next request — otherwise it returns
	// without ever answering textDocument/formatting, and the real
	// client's write for that request then blocks forever on the
	// unbuffered net.Pipe with no reader left on the other end.
	for i := 0; i < 10; i++ {
		body, err := readFramedTestMessage(br)
		if err != nil {
			return
		}
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.Unmarshal(body, &msg); err != nil {
			t.Errorf("fake server: unmarshal request: %v", err)
			return
		}
		if len(msg.ID) == 0 {
			// Notification (e.g. "initialized") — no response expected.
			continue
		}
		switch msg.Method {
		case "initialize":
			err = writeFramedTestMessage(conn, map[string]any{
				"jsonrpc": "2.0",
				"id":      msg.ID,
				"result": map[string]any{
					"capabilities": map[string]any{
						"documentFormattingProvider": true,
					},
				},
			})
		case "textDocument/formatting":
			if err := writeFramedTestMessage(conn, map[string]any{
				"jsonrpc": "2.0",
				"id":      msg.ID,
				"error": map[string]any{
					"code":    jsonrpcMethodNotFound,
					"message": "Method not found: textDocument/formatting",
				},
			}); err != nil {
				t.Errorf("fake server: write response: %v", err)
			}
			return
		default:
			t.Errorf("fake server: unexpected request method %q", msg.Method)
			return
		}
		if err != nil {
			t.Errorf("fake server: write response: %v", err)
			return
		}
	}
}

func readFramedTestMessage(br *bufio.Reader) ([]byte, error) {
	contentLen := 0
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			break
		}
		if strings.HasPrefix(line, "Content-Length:") {
			v := strings.TrimSpace(strings.TrimPrefix(line, "Content-Length:"))
			contentLen, _ = strconv.Atoi(v)
		}
	}
	body := make([]byte, contentLen)
	if _, err := io.ReadFull(br, body); err != nil {
		return nil, err
	}
	return body, nil
}

func writeFramedTestMessage(w io.Writer, v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "Content-Length: %d\r\n\r\n%s", len(body), body)
	return err
}
