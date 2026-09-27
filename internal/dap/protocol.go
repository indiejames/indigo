// Package dap is a client for the Debug Adapter Protocol — to debuggers what
// LSP is to language servers. One client, and every language's adapter (dlv
// for Go, debugpy, codelldb, js-debug) plugs in.
//
// The framing is LSP's (a Content-Length header), but the envelope is not
// JSON-RPC: every message carries a seq and a type (request, response or
// event), and a response names the request_seq it answers and whether it
// succeeded. So this is its own small client rather than a reuse of
// internal/lsp's.
//
// Only the subset of the protocol indigo uses is modelled. Lines and columns
// are 1-based on the wire (initialize says so); converting to indigo's 0-based
// positions is the caller's job, done once at the server boundary.
package dap

import "encoding/json"

// message is the envelope every DAP message shares. Which fields are set
// depends on Type.
type message struct {
	Seq  int    `json:"seq"`
	Type string `json:"type"` // "request", "response" or "event"

	// Requests (ours, or the adapter's reverse requests).
	Command   string          `json:"command,omitempty"`
	Arguments json.RawMessage `json:"arguments,omitempty"`

	// Responses.
	RequestSeq int    `json:"request_seq,omitempty"`
	Success    bool   `json:"success,omitempty"`
	Message    string `json:"message,omitempty"`

	// Events.
	Event string `json:"event,omitempty"`

	// Responses and events.
	Body json.RawMessage `json:"body,omitempty"`
}

// Event is an event from the adapter. Decode Body with the matching *Event type.
type Event struct {
	Event string
	Body  json.RawMessage
}

// ---- initialize ----

// InitializeArguments describes this client to the adapter.
type InitializeArguments struct {
	ClientID        string `json:"clientID"`
	ClientName      string `json:"clientName"`
	AdapterID       string `json:"adapterID"`
	Locale          string `json:"locale,omitempty"`
	PathFormat      string `json:"pathFormat"`
	LinesStartAt1   bool   `json:"linesStartAt1"`
	ColumnsStartAt1 bool   `json:"columnsStartAt1"`

	SupportsVariableType         bool `json:"supportsVariableType"`
	SupportsRunInTerminalRequest bool `json:"supportsRunInTerminalRequest"`
}

// Capabilities is the subset of the adapter's capabilities indigo consults.
type Capabilities struct {
	SupportsConfigurationDoneRequest bool `json:"supportsConfigurationDoneRequest"`
	SupportsConditionalBreakpoints   bool `json:"supportsConditionalBreakpoints"`
	SupportsLogPoints                bool `json:"supportsLogPoints"`
	SupportsEvaluateForHovers        bool `json:"supportsEvaluateForHovers"`
	SupportsTerminateRequest         bool `json:"supportsTerminateRequest"`
}

// ---- breakpoints ----

// Source identifies a file.
type Source struct {
	Name string `json:"name,omitempty"`
	Path string `json:"path,omitempty"`
}

// SourceBreakpoint is a breakpoint as the client asks for it.
type SourceBreakpoint struct {
	Line       int    `json:"line"`
	Condition  string `json:"condition,omitempty"`
	LogMessage string `json:"logMessage,omitempty"`
}

// SetBreakpointsArguments replaces every breakpoint in one file.
type SetBreakpointsArguments struct {
	Source      Source             `json:"source"`
	Breakpoints []SourceBreakpoint `json:"breakpoints"`
}

// Breakpoint is the adapter's view of a breakpoint: whether it could be set
// (Verified), and where it actually landed, which can differ from the line
// asked for (a breakpoint on a blank line moves to the next statement).
type Breakpoint struct {
	ID       int    `json:"id,omitempty"`
	Verified bool   `json:"verified"`
	Line     int    `json:"line,omitempty"`
	Message  string `json:"message,omitempty"`
}

// BreakpointEvent is the adapter changing a breakpoint after the fact —
// js-debug verifies breakpoints this way once the script they are in loads.
type BreakpointEvent struct {
	Reason     string     `json:"reason"` // "changed", "new", "removed"
	Breakpoint Breakpoint `json:"breakpoint"`
}

type setBreakpointsBody struct {
	Breakpoints []Breakpoint `json:"breakpoints"`
}

// ---- execution ----

// Thread is one thread (in Go, one goroutine) of the debuggee.
type Thread struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type threadsBody struct {
	Threads []Thread `json:"threads"`
}

type threadArguments struct {
	ThreadID int `json:"threadId"`
}

// ---- inspection ----

// StackFrame is one frame of a thread's call stack.
type StackFrame struct {
	ID     int     `json:"id"`
	Name   string  `json:"name"`
	Source *Source `json:"source,omitempty"`
	Line   int     `json:"line"`
	Column int     `json:"column"`
}

type stackTraceArguments struct {
	ThreadID   int `json:"threadId"`
	StartFrame int `json:"startFrame,omitempty"`
	Levels     int `json:"levels,omitempty"`
}

type stackTraceBody struct {
	StackFrames []StackFrame `json:"stackFrames"`
	TotalFrames int          `json:"totalFrames,omitempty"`
}

// Scope is a group of variables in a frame (locals, arguments, globals…).
// VariablesReference fetches its contents with Variables.
type Scope struct {
	Name               string `json:"name"`
	VariablesReference int    `json:"variablesReference"`
	Expensive          bool   `json:"expensive"`
}

type scopesBody struct {
	Scopes []Scope `json:"scopes"`
}

// Variable is one value. A non-zero VariablesReference means it has children
// (fields, elements) to fetch with Variables.
type Variable struct {
	Name               string `json:"name"`
	Value              string `json:"value"`
	Type               string `json:"type,omitempty"`
	VariablesReference int    `json:"variablesReference"`
}

type variablesBody struct {
	Variables []Variable `json:"variables"`
}

// Evaluate contexts: where an expression came from, which some adapters use to
// decide how much to show or whether side effects are allowed.
const (
	EvalHover = "hover"
	EvalWatch = "watch"
	EvalREPL  = "repl"
)

type evaluateArguments struct {
	Expression string `json:"expression"`
	// FrameID is a pointer because 0 is a real frame id to some adapters
	// (js-debug numbers from 0); nil evaluates in the global scope.
	FrameID *int   `json:"frameId,omitempty"`
	Context string `json:"context,omitempty"`
}

// EvaluateResult is the value of an evaluated expression.
type EvaluateResult struct {
	Result             string `json:"result"`
	Type               string `json:"type,omitempty"`
	VariablesReference int    `json:"variablesReference"`
}

type disconnectArguments struct {
	TerminateDebuggee bool `json:"terminateDebuggee"`
}

// ---- events ----

// StoppedEvent says the debuggee stopped, and why ("breakpoint", "step",
// "pause", "exception", "entry"…).
type StoppedEvent struct {
	Reason            string `json:"reason"`
	Description       string `json:"description,omitempty"`
	ThreadID          int    `json:"threadId,omitempty"`
	AllThreadsStopped bool   `json:"allThreadsStopped,omitempty"`
	Text              string `json:"text,omitempty"`
}

// ContinuedEvent says the debuggee resumed without being asked to by us.
type ContinuedEvent struct {
	ThreadID            int  `json:"threadId"`
	AllThreadsContinued bool `json:"allThreadsContinued,omitempty"`
}

// ExitedEvent carries the debuggee's exit code.
type ExitedEvent struct {
	ExitCode int `json:"exitCode"`
}

// OutputEvent is text from the debuggee or the adapter. Category is "stdout",
// "stderr", "console" (the adapter's own messages) or "important".
type OutputEvent struct {
	Category string `json:"category,omitempty"`
	Output   string `json:"output"`
}

// Decode unmarshals e.Body into v (one of the *Event types).
func (e Event) Decode(v any) error {
	if len(e.Body) == 0 {
		return nil
	}
	return json.Unmarshal(e.Body, v)
}
