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
}

func newJSONRPCConn(r io.Reader, w io.Writer, handler notificationHandler, reqHandler requestHandler) *jsonrpcConn {
	c := &jsonrpcConn{w: w, handler: handler, reqHandler: reqHandler}
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

	if err := c.write(msg); err != nil {
		return nil, err
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
