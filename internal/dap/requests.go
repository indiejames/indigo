package dap

import (
	"context"
	"encoding/json"
	"fmt"
)

// Initialize is the first request of every session. Lines and columns are
// declared 1-based, the protocol's default and what every adapter handles.
func (c *Client) Initialize(ctx context.Context, adapterID string) (Capabilities, error) {
	body, err := c.Request(ctx, "initialize", InitializeArguments{
		ClientID:        "indigo",
		ClientName:      "indigo",
		AdapterID:       adapterID,
		Locale:          "en",
		PathFormat:      "path",
		LinesStartAt1:   true,
		ColumnsStartAt1: true,

		SupportsVariableType: true,
		// runInTerminal is a reverse request asking the client to start the
		// debuggee in a terminal; indigo has none to offer, so it says so and
		// the adapter starts the process itself.
		SupportsRunInTerminalRequest: false,
	})
	if err != nil {
		return Capabilities{}, err
	}
	var caps Capabilities
	if len(body) > 0 {
		if err := json.Unmarshal(body, &caps); err != nil {
			return Capabilities{}, fmt.Errorf("dap initialize: %w", err)
		}
	}
	return caps, nil
}

// LaunchSession starts the debuggee, running the startup handshake in the
// order the protocol requires:
//
//	launch  →  (adapter sends "initialized")  →  configure  →  configurationDone
//	        →  (adapter answers launch)
//
// This ordering is the classic DAP-client bug. Many adapters, Delve among them,
// do not answer launch until configuration is done, so a client that waits for
// the launch response before setting breakpoints deadlocks; and "initialized"
// can arrive before the launch response, so it has to be watched for from the
// moment launch is sent. configure is where breakpoints are set; it runs after
// "initialized", before configurationDone.
//
// args is the adapter-specific launch configuration.
func (c *Client) LaunchSession(ctx context.Context, caps Capabilities, args any, configure func(context.Context) error) error {
	return c.StartSession(ctx, caps, "launch", args, configure)
}

// StartSession is LaunchSession with the request named: "launch", or "attach"
// — what a child session an adapter asked for with startDebugging may need.
func (c *Client) StartSession(ctx context.Context, caps Capabilities, request string, args any, configure func(context.Context) error) error {
	launchErr := make(chan error, 1)
	go func() {
		_, err := c.Request(ctx, request, args)
		launchErr <- err
	}()

	select {
	case <-c.initialized:
	case err := <-launchErr:
		// launch answered first. An error means the launch failed (a build
		// error, a missing program) and there is nothing to configure;
		// success means this adapter answers launch early, and "initialized"
		// is still to come.
		if err != nil {
			return err
		}
		launchErr <- nil
		select {
		case <-c.initialized:
		case <-ctx.Done():
			return ctx.Err()
		case <-c.done:
			return ErrClosed
		}
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return ErrClosed
	}

	if configure != nil {
		if err := configure(ctx); err != nil {
			return err
		}
	}
	if caps.SupportsConfigurationDoneRequest {
		if _, err := c.Request(ctx, "configurationDone", nil); err != nil {
			return err
		}
	}
	select {
	case err := <-launchErr:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// SetBreakpoints replaces every breakpoint in path with lines (1-based), and
// returns where the adapter actually put each one, in the same order.
func (c *Client) SetBreakpoints(ctx context.Context, path string, bps []SourceBreakpoint) ([]Breakpoint, error) {
	if bps == nil {
		bps = []SourceBreakpoint{} // an empty list clears the file; null is not the same
	}
	body, err := c.Request(ctx, "setBreakpoints", SetBreakpointsArguments{
		Source:      Source{Path: path},
		Breakpoints: bps,
	})
	if err != nil {
		return nil, err
	}
	var b setBreakpointsBody
	if err := json.Unmarshal(body, &b); err != nil {
		return nil, fmt.Errorf("dap setBreakpoints: %w", err)
	}
	return b.Breakpoints, nil
}

// Threads lists the debuggee's threads.
func (c *Client) Threads(ctx context.Context) ([]Thread, error) {
	body, err := c.Request(ctx, "threads", nil)
	if err != nil {
		return nil, err
	}
	var b threadsBody
	if err := json.Unmarshal(body, &b); err != nil {
		return nil, fmt.Errorf("dap threads: %w", err)
	}
	return b.Threads, nil
}

// StackTrace returns up to levels frames of thread's stack (0 = all).
func (c *Client) StackTrace(ctx context.Context, threadID, levels int) ([]StackFrame, error) {
	body, err := c.Request(ctx, "stackTrace", stackTraceArguments{ThreadID: threadID, Levels: levels})
	if err != nil {
		return nil, err
	}
	var b stackTraceBody
	if err := json.Unmarshal(body, &b); err != nil {
		return nil, fmt.Errorf("dap stackTrace: %w", err)
	}
	return b.StackFrames, nil
}

// Scopes returns a frame's variable groups.
func (c *Client) Scopes(ctx context.Context, frameID int) ([]Scope, error) {
	body, err := c.Request(ctx, "scopes", struct {
		FrameID int `json:"frameId"`
	}{frameID})
	if err != nil {
		return nil, err
	}
	var b scopesBody
	if err := json.Unmarshal(body, &b); err != nil {
		return nil, fmt.Errorf("dap scopes: %w", err)
	}
	return b.Scopes, nil
}

// Variables returns the children of a variables reference (a scope, or a
// structured value).
func (c *Client) Variables(ctx context.Context, ref int) ([]Variable, error) {
	body, err := c.Request(ctx, "variables", struct {
		VariablesReference int `json:"variablesReference"`
	}{ref})
	if err != nil {
		return nil, err
	}
	var b variablesBody
	if err := json.Unmarshal(body, &b); err != nil {
		return nil, fmt.Errorf("dap variables: %w", err)
	}
	return b.Variables, nil
}

// Evaluate evaluates expr in frameID (0 = the adapter's default, usually the
// top frame of the stopped thread). evalContext is one of the Eval* constants.
func (c *Client) Evaluate(ctx context.Context, expr string, frameID int, evalContext string) (EvaluateResult, error) {
	return c.evaluate(ctx, expr, &frameID, evalContext)
}

// EvaluateGlobal evaluates expr with no frame: in the global scope.
func (c *Client) EvaluateGlobal(ctx context.Context, expr string, evalContext string) (EvaluateResult, error) {
	return c.evaluate(ctx, expr, nil, evalContext)
}

func (c *Client) evaluate(ctx context.Context, expr string, frameID *int, evalContext string) (EvaluateResult, error) {
	body, err := c.Request(ctx, "evaluate", evaluateArguments{Expression: expr, FrameID: frameID, Context: evalContext})
	if err != nil {
		return EvaluateResult{}, err
	}
	var r EvaluateResult
	if err := json.Unmarshal(body, &r); err != nil {
		return EvaluateResult{}, fmt.Errorf("dap evaluate: %w", err)
	}
	return r, nil
}

// Continue resumes thread (and, for most adapters including Delve, all others).
func (c *Client) Continue(ctx context.Context, threadID int) error {
	_, err := c.Request(ctx, "continue", threadArguments{threadID})
	return err
}

// Next steps over one line in thread.
func (c *Client) Next(ctx context.Context, threadID int) error {
	_, err := c.Request(ctx, "next", threadArguments{threadID})
	return err
}

// StepIn steps into the call on the current line.
func (c *Client) StepIn(ctx context.Context, threadID int) error {
	_, err := c.Request(ctx, "stepIn", threadArguments{threadID})
	return err
}

// StepOut runs until the current function returns.
func (c *Client) StepOut(ctx context.Context, threadID int) error {
	_, err := c.Request(ctx, "stepOut", threadArguments{threadID})
	return err
}

// Pause stops thread.
func (c *Client) Pause(ctx context.Context, threadID int) error {
	_, err := c.Request(ctx, "pause", threadArguments{threadID})
	return err
}

// Disconnect ends the session, terminating a launched debuggee.
func (c *Client) Disconnect(ctx context.Context, terminateDebuggee bool) error {
	_, err := c.Request(ctx, "disconnect", disconnectArguments{TerminateDebuggee: terminateDebuggee})
	return err
}
