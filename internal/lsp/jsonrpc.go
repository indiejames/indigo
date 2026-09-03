package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

type jsonrpcMsg struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *jsonrpcError   `json:"error,omitempty"`
}

type jsonrpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *jsonrpcError) Error() string { return fmt.Sprintf("jsonrpc error %d: %s", e.Code, e.Message) }

// jsonrpcMethodNotFound is the standard JSON-RPC error code a server
// returns for a request it doesn't implement. Some servers advertise a
// capability in their initialize response but don't actually implement the
// corresponding request (observed with Godot's GDScript language server
// and textDocument/formatting: it sets documentFormattingProvider but
// returns this on an actual format request) — callers that already gate on
// the capability may still want to treat this specific error as "not
// supported" rather than a hard failure, as a defensive fallback.
const jsonrpcMethodNotFound = -32601

// notificationHandler is called for incoming notifications (no id).
type notificationHandler func(method string, params json.RawMessage)

// requestHandler is called for incoming server-to-client requests. The
// returned value is marshaled as the JSON-RPC result (nil marshals to
// `null`, matching the old blanket-ack behavior); a non-nil error is sent
// back as a JSON-RPC error instead.
type requestHandler func(method string, params json.RawMessage) (any, error)

type jsonrpcConn struct {
	w          io.Writer
	wMu        sync.Mutex
	nextID     atomic.Int64
	pending    sync.Map // int64 → chan *jsonrpcMsg
	handler    notificationHandler
	reqHandler requestHandler
	onClose    func() // called once, after readLoop exits (EOF/process died)

	// writeSem admits at most one in-flight Call write-goroutine at a time
	// (see Call below): against a permanently stuck connection, every
	// timed-out Call would otherwise spawn its own goroutine that also
	// blocks forever on wMu behind the first stuck write — an unbounded
	// leak given automatic background polling (InlayHints,
	// SemanticTokensRange) that calls Call every few seconds per open
	// buffer against a hung language server.
	writeSem chan struct{}
}

func newJSONRPCConn(r io.Reader, w io.Writer, handler notificationHandler, reqHandler requestHandler) *jsonrpcConn {
	return newJSONRPCConnWithClose(r, w, handler, reqHandler, nil)
}

// newJSONRPCConnWithClose is newJSONRPCConn plus an onClose hook. onClose
// must be provided here rather than set on the returned *jsonrpcConn
// afterward — readLoop starts running in its own goroutine immediately, so
// a field assigned after construction could race a connection that closes
// (or a process that crashes) fast enough to exit readLoop first.
func newJSONRPCConnWithClose(r io.Reader, w io.Writer, handler notificationHandler, reqHandler requestHandler, onClose func()) *jsonrpcConn {
	c := &jsonrpcConn{w: w, handler: handler, reqHandler: reqHandler, onClose: onClose, writeSem: make(chan struct{}, 1)}
	go c.readLoop(r)
	return c
}

// Call sends a request and waits for the response.
func (c *jsonrpcConn) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := c.nextID.Add(1)
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	msg := jsonrpcMsg{JSONRPC: "2.0", ID: &id, Method: method, Params: raw}
	ch := make(chan *jsonrpcMsg, 1)
	c.pending.Store(id, ch)
	defer c.pending.Delete(id)

	// write runs in its own goroutine so ctx actually bounds this call's
	// total wait time: write() can block indefinitely (an unbuffered pipe
	// to a language server subprocess that isn't draining its stdin — e.g.
	// busy re-indexing after a burst of external file changes — or waiting
	// on wMu behind another in-flight write stuck the same way), and that
	// block happens *before* the ctx-aware select below ever runs. Without
	// this, a caller's configured timeout (e.g. Hover's 3s) is meaningless
	// whenever the write itself is what's stuck, not the response.
	// writeErr is buffered so this goroutine can always send and exit even
	// if Call has already returned via ctx.Done() below — it deliberately
	// leaks (keeps running until the write eventually completes or the
	// process dies) rather than blocking the caller; see PLAN.md for why
	// this tradeoff was chosen over the alternative (an OS write deadline,
	// not reliably supported across every io.Writer this connects to).
	//
	// writeSem bounds how many such leaked goroutines can accumulate: it's
	// acquired before spawning one and released once the write finishes, so
	// at most one can ever be stuck at a time per connection. Acquiring it
	// is itself ctx-aware — once one write-goroutine is stuck, every further
	// timed-out Call against the same connection gives up here instead of
	// piling on another goroutine that would also block forever.
	select {
	case c.writeSem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	writeErr := make(chan error, 1)
	go func() {
		defer func() { <-c.writeSem }()
		writeErr <- c.write(msg)
	}()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case err := <-writeErr:
		if err != nil {
			return nil, err
		}
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case resp := <-ch:
		if resp.Error != nil {
			return nil, resp.Error
		}
		return resp.Result, nil
	}
}

// failPending wakes every in-flight Call with a "connection closed" error
// instead of leaving it to block until its own context deadline. Called once
// readLoop exits (EOF, i.e. the language server process died or its pipe
// closed) so callers fail fast rather than waiting out a timeout that can
// never be satisfied.
func (c *jsonrpcConn) failPending() {
	c.pending.Range(func(key, value any) bool {
		ch := value.(chan *jsonrpcMsg)
		select {
		case ch <- &jsonrpcMsg{Error: &jsonrpcError{Code: -32000, Message: "connection closed"}}:
		default:
		}
		return true
	})
}

// failPendingForMalformed handles a body that failed to unmarshal into
// jsonrpcMsg. The most common real-world cause is a syntactically broken
// "result"/"params" payload inside an otherwise-intact envelope, so the "id"
// field is often still independently recoverable; when it is, and it matches
// a pending Call, that call is failed immediately with a parse error instead
// of silently hanging until its own context deadline. If the id can't be
// recovered (e.g. the body isn't valid JSON at all), the message is dropped
// as before and any affected call falls back to its own timeout.
//
// A recovered id alone isn't enough to know this was meant as a response,
// though: a malformed server-to-client *request* (has both "id" and
// "method", e.g. workspace/applyEdit with a broken params payload) also has
// a recoverable id, and the server chooses that id independently of our own
// nextID counter — it can coincidentally collide with one of our pending
// Call ids, especially early in a session when both sides start counting
// from small integers. Failing a pending call for what was actually a
// request meant for reqHandler would wrongly abort an in-flight Call that's
// still going to get its real response. So the method field is checked too:
// only an envelope that looks like a response (id present, no method) is
// treated as one; anything with a method — even malformed — is left to its
// own fallback (a request the client can't parse just goes unanswered,
// which is what already happens for a body that isn't JSON at all).
func (c *jsonrpcConn) failPendingForMalformed(body []byte) {
	var envelope struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	if json.Unmarshal(body, &envelope) != nil || len(envelope.ID) == 0 || envelope.Method != "" {
		return
	}
	var id int64
	if json.Unmarshal(envelope.ID, &id) != nil {
		return
	}
	if ch, ok := c.pending.Load(id); ok {
		select {
		case ch.(chan *jsonrpcMsg) <- &jsonrpcMsg{Error: &jsonrpcError{Code: -32700, Message: "parse error: malformed response body"}}:
		default:
		}
	}
}

// Notify sends a notification (no response expected).
func (c *jsonrpcConn) Notify(method string, params any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	return c.write(jsonrpcMsg{JSONRPC: "2.0", Method: method, Params: raw})
}

func (c *jsonrpcConn) write(msg jsonrpcMsg) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	c.wMu.Lock()
	defer c.wMu.Unlock()
	_, err = fmt.Fprintf(c.w, "Content-Length: %d\r\n\r\n%s", len(body), body)
	return err
}

func (c *jsonrpcConn) readLoop(r io.Reader) {
	if c.onClose != nil {
		defer c.onClose()
	}
	defer c.failPending()
	br := bufio.NewReader(r)
	for {
		// Read headers until blank line.
		contentLen := 0
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				return
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
		if contentLen == 0 {
			continue
		}
		body := make([]byte, contentLen)
		if _, err := io.ReadFull(br, body); err != nil {
			return
		}
		var msg jsonrpcMsg
		if err := json.Unmarshal(body, &msg); err != nil {
			c.failPendingForMalformed(body)
			continue
		}
		if msg.ID != nil && msg.Method == "" {
			// Response to one of our requests.
			if ch, ok := c.pending.Load(*msg.ID); ok {
				ch.(chan *jsonrpcMsg) <- &msg
			}
		} else if msg.Method != "" && msg.ID == nil {
			// Notification from server.
			if c.handler != nil {
				c.handler(msg.Method, msg.Params)
			}
		} else if msg.ID != nil && msg.Method != "" {
			// Server-to-client request. reqHandler returning (nil, nil) for
			// an unrecognized method still acks with a null result so the
			// server isn't left waiting (e.g. window/workDoneProgress/create).
			go func(msg jsonrpcMsg) {
				var result any
				var handlerErr error
				if c.reqHandler != nil {
					result, handlerErr = c.reqHandler(msg.Method, msg.Params)
				}
				resp := jsonrpcMsg{JSONRPC: "2.0", ID: msg.ID}
				if handlerErr != nil {
					resp.Error = &jsonrpcError{Code: -32603, Message: handlerErr.Error()}
				} else {
					raw, err := json.Marshal(result)
					if err != nil {
						raw = json.RawMessage("null")
					}
					resp.Result = raw
				}
				c.write(resp) //nolint:errcheck
			}(msg)
		}
	}
}
