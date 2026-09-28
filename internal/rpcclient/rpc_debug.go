package rpcclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"capnproto.org/go/capnp/v3"

	proto "github.com/indiejames/indigo/internal/proto"
)

// The debugging calls. The server owns breakpoints and the debug session; see
// internal/debug and internal/server/server_debug.go. Lines are 0-based.
//
// A failure the user should read ("dlv not found", a launch timeout, "the
// program is running") comes back as the returned error, the same as a
// transport failure; the server sends it in a result field precisely so it is
// not mistaken for the connection breaking, and this layer turns it back into
// an error for the caller.

// DebugChangedMsg is pushed when debug state, output or breakpoints moved.
// Refetch whichever sequence number advanced past what was last seen — the
// pushes can arrive out of order, so only a larger number means news.
type DebugChangedMsg struct {
	StateSeq, OutputSeq, BreakpointsSeq uint64
}

func (s *callbackServer) DebugChanged(_ context.Context, call proto.ClientCallback_debugChanged) error {
	a := call.Args()
	s.dispatch(DebugChangedMsg{StateSeq: a.StateSeq(), OutputSeq: a.OutputSeq(), BreakpointsSeq: a.BreakpointsSeq()})
	_, err := call.AllocResults()
	return err
}

// DebugStatus mirrors the server's session status.
type DebugStatus uint16

const (
	DebugInactive DebugStatus = iota
	DebugStarting
	DebugRunning
	DebugStopped
	DebugTerminated
)

func (s DebugStatus) String() string {
	switch s {
	case DebugStarting:
		return "starting"
	case DebugRunning:
		return "running"
	case DebugStopped:
		return "stopped"
	case DebugTerminated:
		return "terminated"
	}
	return "inactive"
}

// DebugState is the session's current state.
type DebugState struct {
	Status      DebugStatus
	Seq         uint64
	ThreadID    int64
	Path        string // top frame of the stopped thread
	Line        int
	Reason      string
	Description string
	ExitCode    int
	HasExitCode bool
	Error       string
}

// DebugBreakpoint is one breakpoint.
type DebugBreakpoint struct {
	Path     string
	Line     int
	Verified bool
	Detail   string // why the adapter could not set it
	// Condition: stop only when true. LogMessage: a logpoint, which prints
	// instead of stopping.
	Condition  string
	LogMessage string
}

// DebugFrame is one stack frame.
type DebugFrame struct {
	ID        int64
	Name      string
	Path      string
	Line, Col int
}

// DebugScope is a group of variables in a frame.
type DebugScope struct {
	Name      string
	Ref       int64
	Expensive bool
}

// DebugVariable is one value; a non-zero Ref has children.
type DebugVariable struct {
	Name, Value, Type string
	Ref               int64
}

// DebugOutputChunk is a numbered piece of program or adapter output.
type DebugOutputChunk struct {
	Seq      uint64
	Category string
	Text     string
}

// DebugConfig is what to debug.
type DebugConfig struct {
	Name       string // a named configuration's name; "" for an ad hoc one
	Adapter    string // "go"
	Mode       string // Delve: "debug" or "test"
	Program    string
	Args       []string
	Cwd        string
	BuildFlags string
	Env        []string // "KEY=value"
	// Launch is merged over the launch request, for adapter-specific
	// settings.
	Launch map[string]any
	// Request is "launch" ("" too) or "attach". An attach goes to a running
	// program: by ProcessID, or through Connect, the host:port of a debug
	// adapter already running (`dlv --headless`).
	Request   string
	Connect   string
	ProcessID int
	// PickProcess: ask which process to attach to when this is started.
	PickProcess bool
}

// DebugProcess is a process the debugger could attach to. GoVersion and
// GoModule are set for a Go program.
type DebugProcess struct {
	PID, PPID int
	Name, Exe string
	Args      []string
	GoVersion string
	GoModule  string
}

// IsGo reports whether p is a Go program.
func (p DebugProcess) IsGo() bool { return p.GoVersion != "" }

// IsNode reports whether p is a Node.js process — including one running
// TypeScript through Node's type stripping or tsx, which are node too.
func (p DebugProcess) IsNode() bool { return p.Name == "node" }

// ListProcesses returns the processes on the server's machine the debugger
// could attach to, Go programs first.
func (r *RPC) ListProcesses(ctx context.Context) ([]DebugProcess, error) {
	fut, rel := r.svc.ListProcesses(ctx, nil)
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		return nil, err
	}
	if err := resultError(res.Error()); err != nil {
		return nil, err
	}
	list, err := res.Processes()
	if err != nil {
		return nil, err
	}
	out := make([]DebugProcess, list.Len())
	for i := range out {
		p := list.At(i)
		name, _ := p.Name()
		exe, _ := p.Exe()
		ver, _ := p.GoVersion()
		mod, _ := p.GoModule()
		out[i] = DebugProcess{PID: int(p.Pid()), PPID: int(p.Ppid()), Name: name, Exe: exe,
			Args: textList(p.Args()), GoVersion: ver, GoModule: mod}
	}
	return out, nil
}

// Describe names cfg for a status line: its name, or what it runs.
func (cfg DebugConfig) Describe() string {
	if cfg.Name != "" {
		return cfg.Name
	}
	switch {
	case cfg.Request == "attach" && cfg.Connect != "":
		return "the debugger at " + cfg.Connect
	case cfg.Request == "attach" && cfg.ProcessID > 0:
		return fmt.Sprintf("process %d", cfg.ProcessID)
	}
	what := filepath.Base(cfg.Program)
	if cfg.Mode == "test" {
		for i, a := range cfg.Args {
			if (a == "-test.run" || a == "-test.bench") && i+1 < len(cfg.Args) && cfg.Args[i+1] != "^$" {
				return strings.Trim(cfg.Args[i+1], "^$") + " in " + what
			}
		}
		return "tests in " + what
	}
	return what
}

// DebugAction is a request to change the program's execution.
type DebugAction uint16

const (
	DebugContinue DebugAction = DebugAction(proto.DebugAction_continue)
	DebugNext     DebugAction = DebugAction(proto.DebugAction_next)
	DebugStepIn   DebugAction = DebugAction(proto.DebugAction_stepIn)
	DebugStepOut  DebugAction = DebugAction(proto.DebugAction_stepOut)
	DebugPause    DebugAction = DebugAction(proto.DebugAction_pause)
	DebugStop     DebugAction = DebugAction(proto.DebugAction_stop)
)

// ToggleBreakpoint adds or removes the breakpoint at path:line, reporting
// whether one is now set.
func (r *RPC) ToggleBreakpoint(ctx context.Context, path string, line int) (bool, error) {
	fut, rel := r.svc.ToggleBreakpoint(ctx, func(p proto.EditorService_toggleBreakpoint_Params) error {
		p.SetLine(uint32(line))
		return p.SetPath(path)
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		return false, err
	}
	return res.Set(), nil
}

// SetBreakpoint sets a breakpoint at path:line with a condition and a log
// message, creating it if there is none. Both empty make a plain breakpoint.
func (r *RPC) SetBreakpoint(ctx context.Context, path string, line int, condition, logMessage string) error {
	fut, rel := r.svc.SetBreakpoint(ctx, func(p proto.EditorService_setBreakpoint_Params) error {
		p.SetLine(uint32(line))
		for _, set := range []func() error{
			func() error { return p.SetPath(path) },
			func() error { return p.SetCondition(condition) },
			func() error { return p.SetLogMessage(logMessage) },
		} {
			if err := set(); err != nil {
				return err
			}
		}
		return nil
	})
	defer rel()
	_, err := fut.Struct()
	return err
}

// ListBreakpoints returns the breakpoints in path ("" for every file) and the
// store's sequence number.
func (r *RPC) ListBreakpoints(ctx context.Context, path string) ([]DebugBreakpoint, uint64, error) {
	fut, rel := r.svc.ListBreakpoints(ctx, func(p proto.EditorService_listBreakpoints_Params) error {
		return p.SetPath(path)
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		return nil, 0, err
	}
	list, err := res.Breakpoints()
	if err != nil {
		return nil, 0, err
	}
	out := make([]DebugBreakpoint, list.Len())
	for i := range out {
		b := list.At(i)
		p, _ := b.Path()
		d, _ := b.Detail()
		cond, _ := b.Condition()
		logMsg, _ := b.LogMessage()
		out[i] = DebugBreakpoint{Path: p, Line: int(b.Line()), Verified: b.Verified(), Detail: d, Condition: cond, LogMessage: logMsg}
	}
	return out, res.Seq(), nil
}

// DebugStart launches cfg and returns once the program is running.
func (r *RPC) DebugStart(ctx context.Context, cfg DebugConfig) error {
	_, err := r.DebugStartWithWarning(ctx, cfg)
	return err
}

// DebugStartWithWarning is DebugStart, also returning the server's warning
// about a session that started but will not fully work ("" for none).
func (r *RPC) DebugStartWithWarning(ctx context.Context, cfg DebugConfig) (string, error) {
	fut, rel := r.svc.DebugStart(ctx, func(p proto.EditorService_debugStart_Params) error {
		pc, err := p.NewConfig()
		if err != nil {
			return err
		}
		return writeDebugConfig(pc, cfg)
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		return "", err
	}
	if err := resultError(res.Error()); err != nil {
		return "", err
	}
	warning, _ := res.Warning()
	return warning, nil
}

// DebugConfigs lists the workspace's named debug configurations; activeFile is
// what launch.json's ${file} means. A non-nil
// error with configurations means some file could not be read; the
// configurations are the ones that could.
func (r *RPC) DebugConfigs(ctx context.Context, activeFile string) ([]DebugConfig, error) {
	fut, rel := r.svc.DebugConfigs(ctx, func(p proto.EditorService_debugConfigs_Params) error {
		return p.SetActiveFile(activeFile)
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		return nil, err
	}
	var out []DebugConfig
	if list, err := res.Configs(); err == nil {
		for i := 0; i < list.Len(); i++ {
			out = append(out, readDebugConfig(list.At(i)))
		}
	}
	return out, resultError(res.Error())
}

// DebugRestart stops any running session and starts the most recently started
// configuration again, or fallback when none has been. It returns the
// configuration launched — also when the launch fails, so the caller can say
// what failed.
func (r *RPC) DebugRestart(ctx context.Context, fallback DebugConfig) (DebugConfig, error) {
	fut, rel := r.svc.DebugRestart(ctx, func(p proto.EditorService_debugRestart_Params) error {
		pc, err := p.NewFallback()
		if err != nil {
			return err
		}
		return writeDebugConfig(pc, fallback)
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		return DebugConfig{}, err
	}
	var started DebugConfig
	if pc, err := res.Started(); err == nil {
		started = readDebugConfig(pc)
	}
	return started, resultError(res.Error())
}

func writeDebugConfig(pc proto.DebugConfig, cfg DebugConfig) error {
	for _, set := range []func() error{
		func() error { return pc.SetName(cfg.Name) },
		func() error { return pc.SetAdapter(cfg.Adapter) },
		func() error { return pc.SetMode(cfg.Mode) },
		func() error { return pc.SetProgram(cfg.Program) },
		func() error { return pc.SetCwd(cfg.Cwd) },
		func() error { return pc.SetBuildFlags(cfg.BuildFlags) },
		func() error { return setTextList(cfg.Args, pc.NewArgs) },
		func() error { return setTextList(cfg.Env, pc.NewEnv) },
		func() error { return setLaunch(pc, cfg.Launch) },
		func() error { return pc.SetRequest(cfg.Request) },
		func() error { return pc.SetConnect(cfg.Connect) },
		func() error { pc.SetProcessId(int64(cfg.ProcessID)); return nil },
		func() error { pc.SetPickProcess(cfg.PickProcess); return nil },
	} {
		if err := set(); err != nil {
			return err
		}
	}
	return nil
}

func setTextList(items []string, alloc func(int32) (capnp.TextList, error)) error {
	if len(items) == 0 {
		return nil
	}
	list, err := alloc(int32(len(items)))
	if err != nil {
		return err
	}
	for i, it := range items {
		if err := list.Set(i, it); err != nil {
			return err
		}
	}
	return nil
}

func readDebugConfig(pc proto.DebugConfig) DebugConfig {
	var cfg DebugConfig
	cfg.Name, _ = pc.Name()
	cfg.Adapter, _ = pc.Adapter()
	cfg.Mode, _ = pc.Mode()
	cfg.Program, _ = pc.Program()
	cfg.Cwd, _ = pc.Cwd()
	cfg.BuildFlags, _ = pc.BuildFlags()
	cfg.Args = textList(pc.Args())
	cfg.Env = textList(pc.Env())
	cfg.Launch = decodeLaunch(pc.LaunchJson())
	cfg.Request, _ = pc.Request()
	cfg.Connect, _ = pc.Connect()
	cfg.ProcessID = int(pc.ProcessId())
	cfg.PickProcess = pc.PickProcess()
	return cfg
}

func textList(list capnp.TextList, err error) []string {
	if err != nil {
		return nil
	}
	var out []string
	for i := 0; i < list.Len(); i++ {
		a, _ := list.At(i)
		out = append(out, a)
	}
	return out
}

// DebugControl continues, steps, pauses or stops the program.
func (r *RPC) DebugControl(ctx context.Context, action DebugAction) error {
	fut, rel := r.svc.DebugControl(ctx, func(p proto.EditorService_debugControl_Params) error {
		p.SetAction(proto.DebugAction(action))
		return nil
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		return err
	}
	return resultError(res.Error())
}

// DebugState returns the session's current state.
func (r *RPC) DebugState(ctx context.Context) (DebugState, error) {
	fut, rel := r.svc.DebugState(ctx, func(proto.EditorService_debugState_Params) error { return nil })
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		return DebugState{}, err
	}
	ps, err := res.State()
	if err != nil {
		return DebugState{}, err
	}
	path, _ := ps.Path()
	reason, _ := ps.Reason()
	desc, _ := ps.Description()
	errText, _ := ps.Error()
	return DebugState{
		Status:      DebugStatus(ps.Status()),
		Seq:         ps.Seq(),
		ThreadID:    ps.ThreadId(),
		Path:        path,
		Line:        int(ps.Line()),
		Reason:      reason,
		Description: desc,
		ExitCode:    int(ps.ExitCode()),
		HasExitCode: ps.HasExitCode(),
		Error:       errText,
	}, nil
}

// DebugStackTrace returns thread's stack (0: the stopped thread).
func (r *RPC) DebugStackTrace(ctx context.Context, threadID int64) ([]DebugFrame, error) {
	fut, rel := r.svc.DebugStackTrace(ctx, func(p proto.EditorService_debugStackTrace_Params) error {
		p.SetThreadId(threadID)
		return nil
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		return nil, err
	}
	if err := resultError(res.Error()); err != nil {
		return nil, err
	}
	list, err := res.Frames()
	if err != nil {
		return nil, err
	}
	out := make([]DebugFrame, list.Len())
	for i := range out {
		f := list.At(i)
		name, _ := f.Name()
		path, _ := f.Path()
		out[i] = DebugFrame{ID: f.Id(), Name: name, Path: path, Line: int(f.Line()), Col: int(f.Col())}
	}
	return out, nil
}

// DebugScopes returns a frame's variable groups.
func (r *RPC) DebugScopes(ctx context.Context, frameID int64) ([]DebugScope, error) {
	fut, rel := r.svc.DebugScopes(ctx, func(p proto.EditorService_debugScopes_Params) error {
		p.SetFrameId(frameID)
		return nil
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		return nil, err
	}
	if err := resultError(res.Error()); err != nil {
		return nil, err
	}
	list, err := res.Scopes()
	if err != nil {
		return nil, err
	}
	out := make([]DebugScope, list.Len())
	for i := range out {
		sc := list.At(i)
		name, _ := sc.Name()
		out[i] = DebugScope{Name: name, Ref: sc.Ref(), Expensive: sc.Expensive()}
	}
	return out, nil
}

// DebugVariables returns the children of ref.
func (r *RPC) DebugVariables(ctx context.Context, ref int64) ([]DebugVariable, error) {
	fut, rel := r.svc.DebugVariables(ctx, func(p proto.EditorService_debugVariables_Params) error {
		p.SetRef(ref)
		return nil
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		return nil, err
	}
	if err := resultError(res.Error()); err != nil {
		return nil, err
	}
	list, err := res.Variables()
	if err != nil {
		return nil, err
	}
	out := make([]DebugVariable, list.Len())
	for i := range out {
		out[i] = debugVariable(list.At(i))
	}
	return out, nil
}

// DebugEvaluate evaluates expr in frameID (0: the stopped thread's top frame).
// evalContext is "hover", "watch" or "repl".
func (r *RPC) DebugEvaluate(ctx context.Context, expr string, frameID int64, evalContext string) (DebugVariable, error) {
	fut, rel := r.svc.DebugEvaluate(ctx, func(p proto.EditorService_debugEvaluate_Params) error {
		p.SetFrameId(frameID)
		p.SetContext(evalContext) //nolint:errcheck
		return p.SetExpression(expr)
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		return DebugVariable{}, err
	}
	if err := resultError(res.Error()); err != nil {
		return DebugVariable{}, err
	}
	v, err := res.Result()
	if err != nil {
		return DebugVariable{}, err
	}
	return debugVariable(v), nil
}

// DebugOutput returns output chunks after sinceSeq, the latest seq, and
// whether some were dropped before this call could see them.
func (r *RPC) DebugOutput(ctx context.Context, sinceSeq uint64) ([]DebugOutputChunk, uint64, bool, error) {
	fut, rel := r.svc.DebugOutput(ctx, func(p proto.EditorService_debugOutput_Params) error {
		p.SetSinceSeq(sinceSeq)
		return nil
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		return nil, 0, false, err
	}
	list, err := res.Chunks()
	if err != nil {
		return nil, 0, false, err
	}
	out := make([]DebugOutputChunk, list.Len())
	for i := range out {
		c := list.At(i)
		cat, _ := c.Category()
		text, _ := c.Text()
		out[i] = DebugOutputChunk{Seq: c.Seq(), Category: cat, Text: text}
	}
	return out, res.Latest(), res.Truncated(), nil
}

func debugVariable(v proto.DebugVariable) DebugVariable {
	name, _ := v.Name()
	value, _ := v.Value()
	typ, _ := v.Type()
	return DebugVariable{Name: name, Value: value, Type: typ, Ref: v.Ref()}
}

// resultError turns a result's error text into an error; "" is success.
func resultError(msg string, err error) error {
	if err != nil {
		return err
	}
	if msg != "" {
		return errors.New(msg)
	}
	return nil
}

// The launch table crosses as JSON: its values are whatever the adapter takes
// (nested tables, lists, numbers), which a capnp struct cannot type.
func setLaunch(pc proto.DebugConfig, launch map[string]any) error {
	if len(launch) == 0 {
		return nil
	}
	b, err := json.Marshal(launch)
	if err != nil {
		return err
	}
	return pc.SetLaunchJson(string(b))
}

func decodeLaunch(s string, err error) map[string]any {
	if err != nil || s == "" {
		return nil
	}
	var m map[string]any
	if json.Unmarshal([]byte(s), &m) != nil {
		return nil
	}
	return m
}
