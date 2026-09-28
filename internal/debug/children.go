package debug

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync/atomic"

	"github.com/indiejames/indigo/internal/dap"
)

// Child sessions. Some adapters do not debug the program on the connection
// the client opened: VS Code's JavaScript debugger (js-debug) answers a Node
// launch by starting the program and then asking the client, with a
// startDebugging reverse request, to open a *second* session on a new
// connection to the same server — and that second session is the one with the
// threads, the stops and the breakpoints. A program that starts more
// processes (workers, child_process) asks again, from the child.
//
// So a session here is a tree: the root connection m.client, which owns the
// adapter process and whose "terminated" ends everything, and any children.
// Requests about the program go to the target — the child that last stopped,
// else the newest child, else the root — and breakpoints go to every
// connection, since each child is a separate program.

// startDebuggingArgs is the startDebugging request's arguments.
type startDebuggingArgs struct {
	Configuration map[string]any `json:"configuration"`
	Request       string         `json:"request"` // "launch" or "attach"
}

// reverseHandler answers the adapter's requests on a session's connections.
func (m *Manager) reverseHandler(gen int, adapterID string) dap.ReverseHandler {
	return func(command string, raw json.RawMessage) (any, error) {
		if command != "startDebugging" {
			return nil, fmt.Errorf("%s is not supported by indigo", command)
		}
		var args startDebuggingArgs
		if err := json.Unmarshal(raw, &args); err != nil {
			return nil, err
		}
		if args.Request != "launch" && args.Request != "attach" {
			return nil, fmt.Errorf("startDebugging: unknown request %q", args.Request)
		}
		m.mu.Lock()
		root := m.client
		current := gen == m.gen
		m.mu.Unlock()
		if !current || root == nil {
			return nil, ErrNoSession
		}
		if root.Addr() == "" {
			// A child session is a new connection to the adapter's server;
			// an adapter on stdio has nowhere to connect to.
			return nil, errors.New("startDebugging needs an adapter reached over TCP")
		}
		// Answered at once, the child started in the background: the adapter
		// may not proceed with the child's own requests until it has its
		// answer, and the child's launch does not return until configured.
		go m.startChild(gen, root.Addr(), adapterID, args)
		return nil, nil
	}
}

// startChild opens and configures a child session. A failure is reported in
// the session's console: the program may well be running without it, just not
// under the debugger.
func (m *Manager) startChild(gen int, addr, adapterID string, args startDebuggingArgs) {
	ctx, cancel := context.WithTimeout(context.Background(), m.launchTimeout)
	defer cancel()
	var self atomic.Pointer[dap.Client]
	child, err := dap.Dial(ctx, addr, func(e dap.Event) { m.onEvent(gen, self.Load(), e) })
	if err != nil {
		m.childFailed(gen, err)
		return
	}
	self.Store(child)
	child.SetReverseHandler(m.reverseHandler(gen, adapterID))

	m.mu.Lock()
	if gen != m.gen || m.client == nil {
		m.mu.Unlock()
		child.Close() //nolint:errcheck
		return
	}
	m.children = append(m.children, child)
	m.mu.Unlock()
	go func() {
		<-child.Done()
		m.removeChild(gen, child)
	}()

	caps, err := child.Initialize(ctx, adapterID)
	if err == nil {
		err = child.StartSession(ctx, caps, args.Request, args.Configuration, func(ctx context.Context) error {
			for _, path := range m.Breakpoints.Paths() {
				if err := m.sendBreakpoints(ctx, child, path); err != nil {
					return err
				}
			}
			return nil
		})
	}
	if err != nil {
		m.childFailed(gen, err)
		m.removeChild(gen, child)
		return
	}
	m.fire()
}

func (m *Manager) childFailed(gen int, err error) {
	m.mu.Lock()
	current := gen == m.gen
	m.mu.Unlock()
	if current {
		m.appendOutput("console", fmt.Sprintf("could not start a child debug session: %v\n", err))
		m.fire()
	}
}

// removeChild drops a child session that ended. The session as a whole ends
// when the root says so, not here: the adapter decides whether the program
// is finished (js-debug terminates the root once the last child has gone).
func (m *Manager) removeChild(gen int, child *dap.Client) {
	m.mu.Lock()
	if gen != m.gen {
		m.mu.Unlock()
		return
	}
	i := slices.Index(m.children, child)
	if i < 0 {
		m.mu.Unlock()
		return
	}
	m.children = slices.Delete(m.children, i, i+1)
	if m.active == child {
		m.active = nil
	}
	m.mu.Unlock()
	// A breakpoint is verified while some connection has it set, so this
	// child's answers must not outlive it — otherwise a breakpoint stays
	// drawn as set in a program that has gone.
	m.Breakpoints.ForgetConnection(child)
	m.fire()
	// Closing, not Shutdown: disconnecting a child can terminate the
	// program another child is still debugging.
	go child.Close() //nolint:errcheck
}

// targetLocked is the connection requests about the program go to. Callers
// hold m.mu.
func (m *Manager) targetLocked() *dap.Client {
	if m.active != nil && slices.Contains(m.children, m.active) {
		return m.active
	}
	if n := len(m.children); n > 0 {
		return m.children[n-1]
	}
	return m.client
}

// connections is every connection of the current session, root first.
func (m *Manager) connections() []*dap.Client {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.client == nil {
		return nil
	}
	return append([]*dap.Client{m.client}, m.children...)
}
