package dap

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// ErrClosed is returned for a request that could not complete because the
// connection to the adapter went away.
var ErrClosed = errors.New("dap: connection closed")

// Client is a connection to one debug adapter.
//
// Requests may be made from any goroutine. Events are delivered to the handler
// in the order the adapter sent them, on a goroutine of their own — never on
// the reader. That is what lets a handler react to "stopped" by *requesting*
// the stack: if it ran on the reader, it would wait for a response only the
// reader could deliver.
type Client struct {
	rwc     io.ReadWriteCloser
	handler func(Event)

	seq atomic.Int64
	wMu sync.Mutex

	mu      sync.Mutex
	pending map[int]chan *message
	closed  bool

	// initialized is closed when the adapter sends its "initialized" event.
	// LaunchSession waits on it; it is tracked here, not by the handler,
	// because it can arrive before launch returns and must not be missed.
	initialized     chan struct{}
	initializedOnce sync.Once

	events *eventQueue
	done   chan struct{} // closed when the reader stops
	onEnd  func()        // extra teardown (killing a process), run once by Close
	endMu  sync.Once

	reverse ReverseHandler // guarded by mu
	addr    string         // the TCP address, for an adapter reached over one
}

// ReverseHandler answers a request the adapter sends the client (a "reverse
// request": startDebugging, runInTerminal). The returned body is the
// response's; an error makes it a failure carrying the message.
type ReverseHandler func(command string, args json.RawMessage) (body any, err error)

// SetReverseHandler installs h for the adapter's requests. Without one they
// are answered "not supported".
func (c *Client) SetReverseHandler(h ReverseHandler) {
	c.mu.Lock()
	c.reverse = h
	c.mu.Unlock()
}

// Addr is the TCP address this client reached its adapter at, or "" for one
// over stdio. An adapter that asks for a child session (startDebugging)
// expects it on a new connection to the same address.
func (c *Client) Addr() string { return c.addr }

// NewClient runs the protocol over rwc. handler receives every event and may
// be nil.
func NewClient(rwc io.ReadWriteCloser, handler func(Event)) *Client {
	c := &Client{
		rwc:         rwc,
		handler:     handler,
		pending:     make(map[int]chan *message),
		initialized: make(chan struct{}),
		events:      newEventQueue(),
		done:        make(chan struct{}),
	}
	go c.dispatchEvents()
	go c.readLoop()
	return c
}

// Done is closed once the connection to the adapter has ended.
func (c *Client) Done() <-chan struct{} { return c.done }

// Close ends the connection and whatever process is behind it.
func (c *Client) Close() error {
	err := c.rwc.Close()
	c.endMu.Do(func() {
		if c.onEnd != nil {
			c.onEnd()
		}
	})
	<-c.done
	return err
}

// Request sends command with args and waits for the response, returning its
// body. A response with success:false is returned as an error carrying the
// adapter's message.
func (c *Client) Request(ctx context.Context, command string, args any) (json.RawMessage, error) {
	var raw json.RawMessage
	if args != nil {
		b, err := json.Marshal(args)
		if err != nil {
			return nil, err
		}
		raw = b
	}
	seq := int(c.seq.Add(1))
	ch := make(chan *message, 1)

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, ErrClosed
	}
	c.pending[seq] = ch
	c.mu.Unlock()

	if err := c.write(message{Seq: seq, Type: "request", Command: command, Arguments: raw}); err != nil {
		c.forget(seq)
		return nil, err
	}

	select {
	case resp, ok := <-ch:
		if !ok {
			return nil, ErrClosed
		}
		if !resp.Success {
			msg := resp.Message
			if msg == "" {
				msg = "failed"
			}
			// Many adapters put the useful detail in body.error.format.
			var detail struct {
				Error struct {
					Format string `json:"format"`
				} `json:"error"`
			}
			if json.Unmarshal(resp.Body, &detail) == nil && detail.Error.Format != "" {
				msg += ": " + detail.Error.Format
			}
			return nil, fmt.Errorf("dap %s: %s", command, msg)
		}
		return resp.Body, nil
	case <-ctx.Done():
		c.forget(seq)
		return nil, ctx.Err()
	}
}

func (c *Client) forget(seq int) {
	c.mu.Lock()
	delete(c.pending, seq)
	c.mu.Unlock()
}

func (c *Client) write(m message) error {
	body, err := json.Marshal(m)
	if err != nil {
		return err
	}
	c.wMu.Lock()
	defer c.wMu.Unlock()
	_, err = fmt.Fprintf(c.rwc, "Content-Length: %d\r\n\r\n%s", len(body), body)
	return err
}

func (c *Client) readLoop() {
	// Order matters, as the LSP client learned: pending requests are failed
	// before done is closed, so nothing waiting on a response observes the
	// connection as ended while its request still looks outstanding.
	defer close(c.done)
	defer c.events.close()
	defer c.failPending()

	br := bufio.NewReader(c.rwc)
	for {
		body, err := readFrame(br)
		if err != nil {
			return
		}
		var m message
		if err := json.Unmarshal(body, &m); err != nil {
			continue // a malformed frame; the next one may be fine
		}
		switch m.Type {
		case "response":
			c.mu.Lock()
			ch, ok := c.pending[m.RequestSeq]
			delete(c.pending, m.RequestSeq)
			c.mu.Unlock()
			if ok {
				ch <- &m
			}
		case "event":
			if m.Event == "initialized" {
				c.initializedOnce.Do(func() { close(c.initialized) })
			}
			c.events.push(Event{Event: m.Event, Body: m.Body})
		case "request":
			// A reverse request (runInTerminal, startDebugging). Never
			// ignored: an adapter that sent one is waiting for the reply and
			// would otherwise hang. The handler runs on its own goroutine —
			// it may make requests of its own, whose responses only this
			// reader can deliver.
			c.mu.Lock()
			h := c.reverse
			c.mu.Unlock()
			req := m
			go func() {
				resp := message{Type: "response", RequestSeq: req.Seq, Command: req.Command, Message: "not supported by indigo"}
				if h != nil {
					body, err := h(req.Command, req.Arguments)
					if err != nil {
						resp.Message = err.Error()
					} else {
						resp.Success, resp.Message = true, ""
						if body != nil {
							resp.Body, _ = json.Marshal(body)
						}
					}
				}
				resp.Seq = int(c.seq.Add(1))
				c.write(resp) //nolint:errcheck
			}()
		}
	}
}

func (c *Client) failPending() {
	c.mu.Lock()
	c.closed = true
	for seq, ch := range c.pending {
		close(ch)
		delete(c.pending, seq)
	}
	c.mu.Unlock()
}

func (c *Client) dispatchEvents() {
	for {
		e, ok := c.events.pop()
		if !ok {
			return
		}
		if c.handler != nil {
			c.handler(e)
		}
		c.events.handled()
	}
}

// WaitEventsIdle waits until every event received so far has been handled.
//
// The reader queues each event before it delivers the next response, so once
// a request has returned, every event the adapter sent ahead of that response
// is already queued. This waits for the handler to catch up to them. The case
// it exists for: a failed launch, whose explanation (the compiler's errors)
// arrives as output events just before the failure response — read the
// output too soon and it is not there yet.
func (c *Client) WaitEventsIdle(ctx context.Context) error {
	return c.events.waitIdle(ctx)
}

// readFrame reads one Content-Length framed message body.
func readFrame(br *bufio.Reader) ([]byte, error) {
	contentLen := -1
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			if contentLen < 0 {
				continue // stray blank line between frames
			}
			break
		}
		if name, val, ok := strings.Cut(line, ":"); ok && strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
			n, err := strconv.Atoi(strings.TrimSpace(val))
			if err != nil || n < 0 {
				return nil, fmt.Errorf("dap: bad Content-Length %q", val)
			}
			contentLen = n
		}
	}
	body := make([]byte, contentLen)
	if _, err := io.ReadFull(br, body); err != nil {
		return nil, err
	}
	return body, nil
}

// eventQueue is an unbounded FIFO. Unbounded so the reader never blocks on a
// slow handler — a blocked reader would stop delivering the responses that
// handler may itself be waiting for.
type eventQueue struct {
	mu     sync.Mutex
	cond   *sync.Cond
	items  []Event
	busy   bool // an event has been popped and its handler is running
	closed bool
}

func newEventQueue() *eventQueue {
	q := &eventQueue{}
	q.cond = sync.NewCond(&q.mu)
	return q
}

func (q *eventQueue) push(e Event) {
	q.mu.Lock()
	q.items = append(q.items, e)
	q.mu.Unlock()
	q.cond.Broadcast() // the dispatcher and any waitIdle both wait on cond
}

// handled marks the popped event's handler finished.
func (q *eventQueue) handled() {
	q.mu.Lock()
	q.busy = false
	q.mu.Unlock()
	q.cond.Broadcast()
}

// waitIdle blocks until the queue is empty and no handler is running, or ctx
// ends, or the queue closes.
func (q *eventQueue) waitIdle(ctx context.Context) error {
	stop := context.AfterFunc(ctx, func() {
		q.mu.Lock()
		q.mu.Unlock() //nolint:staticcheck // taken so the Broadcast cannot slip between a waiter's check and its Wait
		q.cond.Broadcast()
	})
	defer stop()
	q.mu.Lock()
	defer q.mu.Unlock()
	for (len(q.items) > 0 || q.busy) && !q.closed {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		q.cond.Wait()
	}
	return nil
}

// pop blocks for the next event; ok is false once the queue is closed and
// drained, so events already received are still delivered after a disconnect
// ("terminated" is usually the last thing an adapter sends).
func (q *eventQueue) pop() (Event, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for len(q.items) == 0 && !q.closed {
		q.cond.Wait()
	}
	if len(q.items) == 0 {
		return Event{}, false
	}
	e := q.items[0]
	q.items = q.items[1:]
	q.busy = true
	return e, true
}

func (q *eventQueue) close() {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
	q.cond.Broadcast()
}
