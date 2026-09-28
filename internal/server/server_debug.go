package server

import (
	"context"
	"encoding/json"
	"time"

	"capnproto.org/go/capnp/v3"

	"github.com/indiejames/indigo/internal/config"
	"github.com/indiejames/indigo/internal/dap"
	"github.com/indiejames/indigo/internal/debug"
	"github.com/indiejames/indigo/internal/document"
	proto "github.com/indiejames/indigo/internal/proto"
)

// The debugging RPCs: thin translation between capnp and debug.Manager, which
// holds the breakpoints and the session. See internal/debug.
//
// Every handler that talks to the adapter calls call.Go() first: a step or an
// evaluate waits on the debugger, and capnp serialises a connection's calls
// until a handler releases the queue — without it one slow evaluate would
// stall every edit and poll on that connection.

// pushDebugChanged tells every window that debug state, output or breakpoints
// moved. It runs on its own goroutine, always: it is reached from edit paths
// that hold s.mu (a server-originated edit shifting a breakpoint), and
// collecting the callbacks takes s.mu. Doing it inline would deadlock, and the
// sequence-number design already tolerates pushes arriving in any order.
func (s *editorService) pushDebugChanged(stateSeq, outputSeq, breakpointsSeq uint64) {
	go func() {
		s.mu.Lock()
		callbacks := s.allCallbacks()
		s.mu.Unlock()
		for _, cb := range callbacks {
			go func(cb proto.ClientCallback) {
				ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
				defer cancel()
				fut, rel := cb.DebugChanged(ctx, func(p proto.ClientCallback_debugChanged_Params) error {
					p.SetStateSeq(stateSeq)
					p.SetOutputSeq(outputSeq)
					p.SetBreakpointsSeq(breakpointsSeq)
					return nil
				})
				fut.Struct() //nolint:errcheck
				rel()
			}(cb)
		}
	}()
}

// shiftBreakpoints keeps path's breakpoints on their lines through ops, which
// have just been applied to its buffer. Called for every applied op, from
// windows and from the server's own edits alike.
func (s *editorService) shiftBreakpoints(path string, ops []document.Op) {
	if s.dbg == nil || path == "" {
		return
	}
	for _, op := range ops {
		s.dbg.ApplyEdit(path, op)
	}
}

func (s *editorService) ToggleBreakpoint(_ context.Context, call proto.EditorService_toggleBreakpoint) error {
	args := call.Args()
	path, err := args.Path()
	if err != nil {
		return err
	}
	res, err := call.AllocResults()
	if err != nil {
		return err
	}
	if s.dbg == nil {
		return nil
	}
	res.SetSet(s.dbg.ToggleBreakpoint(canonicalPath(path), int(args.Line())))
	return nil
}

func (s *editorService) SetBreakpoint(_ context.Context, call proto.EditorService_setBreakpoint) error {
	args := call.Args()
	path, err := args.Path()
	if err != nil {
		return err
	}
	cond, _ := args.Condition()
	logMsg, _ := args.LogMessage()
	if _, err := call.AllocResults(); err != nil {
		return err
	}
	if s.dbg != nil {
		s.dbg.SetBreakpoint(canonicalPath(path), int(args.Line()), cond, logMsg)
	}
	return nil
}

func (s *editorService) ListBreakpoints(_ context.Context, call proto.EditorService_listBreakpoints) error {
	path, err := call.Args().Path()
	if err != nil {
		return err
	}
	res, err := call.AllocResults()
	if err != nil || s.dbg == nil {
		return err
	}
	if path != "" {
		path = canonicalPath(path)
	}
	bps, seq := s.dbg.Breakpoints.List(path)
	res.SetSeq(seq)
	list, err := res.NewBreakpoints(int32(len(bps)))
	if err != nil {
		return err
	}
	for i, bp := range bps {
		item := list.At(i)
		item.SetPath(bp.Path) //nolint:errcheck
		item.SetLine(uint32(bp.Line))
		item.SetVerified(bp.Verified)
		item.SetDetail(bp.Message)        //nolint:errcheck
		item.SetCondition(bp.Condition)   //nolint:errcheck
		item.SetLogMessage(bp.LogMessage) //nolint:errcheck
	}
	return nil
}

func (s *editorService) DebugStart(_ context.Context, call proto.EditorService_debugStart) error {
	pc, err := call.Args().Config()
	if err != nil {
		return err
	}
	cfg := readDebugConfig(pc)
	cfg = s.withDebugDefaults(cfg)
	call.Go() // a launch builds the program: seconds, sometimes minutes

	res, err := call.AllocResults()
	if err != nil {
		return err
	}
	if s.dbg == nil {
		return res.SetError("debugging is not available")
	}
	warning, err := s.dbg.StartWithWarning(cfg)
	if err != nil {
		return res.SetError(err.Error())
	}
	return res.SetWarning(warning)
}

// readDebugConfig converts a capnp DebugConfig. Unreadable fields are left
// empty, which the launch then reports as a missing program.
func readDebugConfig(pc proto.DebugConfig) debug.Config {
	var cfg debug.Config
	cfg.Name, _ = pc.Name()
	cfg.Adapter, _ = pc.Adapter()
	cfg.Mode, _ = pc.Mode()
	cfg.Program, _ = pc.Program()
	cfg.Cwd, _ = pc.Cwd()
	cfg.BuildFlags, _ = pc.BuildFlags()
	cfg.Args = readTextList(pc.Args())
	cfg.Env = readTextList(pc.Env())
	cfg.Launch = decodeLaunch(pc.LaunchJson())
	cfg.Request, _ = pc.Request()
	cfg.Connect, _ = pc.Connect()
	cfg.ProcessID = int(pc.ProcessId())
	cfg.PickProcess = pc.PickProcess()
	cfg.EnvFile, _ = pc.EnvFile()
	return cfg
}

func readTextList(list capnp.TextList, err error) []string {
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

func writeDebugConfig(pc proto.DebugConfig, cfg debug.Config) error {
	for _, set := range []func() error{
		func() error { return pc.SetName(cfg.Name) },
		func() error { return pc.SetAdapter(cfg.Adapter) },
		func() error { return pc.SetMode(cfg.Mode) },
		func() error { return pc.SetProgram(cfg.Program) },
		func() error { return pc.SetCwd(cfg.Cwd) },
		func() error { return pc.SetBuildFlags(cfg.BuildFlags) },
		func() error { return writeTextList(cfg.Args, pc.NewArgs) },
		func() error { return writeTextList(cfg.Env, pc.NewEnv) },
		func() error { return setLaunch(pc, cfg.Launch) },
		func() error { return pc.SetRequest(cfg.Request) },
		func() error { return pc.SetConnect(cfg.Connect) },
		func() error { pc.SetProcessId(int64(cfg.ProcessID)); return nil },
		func() error { pc.SetPickProcess(cfg.PickProcess); return nil },
		func() error { return pc.SetEnvFile(cfg.EnvFile) },
	} {
		if err := set(); err != nil {
			return err
		}
	}
	return nil
}

func writeTextList(items []string, alloc func(int32) (capnp.TextList, error)) error {
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

// withDebugDefaults fills what a configuration left to the workspace.
func (s *editorService) withDebugDefaults(cfg debug.Config) debug.Config {
	if cfg.Cwd == "" {
		cfg.Cwd = s.workspaceDir
	}
	return cfg
}

func (s *editorService) DebugConfigs(_ context.Context, call proto.EditorService_debugConfigs) error {
	activeFile, _ := call.Args().ActiveFile() // read before AllocResults, like every handler here
	res, err := call.AllocResults()
	if err != nil {
		return err
	}
	if s.dbg == nil {
		return res.SetError("debugging is not available")
	}
	var global []config.DebugLaunch
	if s.cfg != nil {
		global = s.cfg.DebugLaunches
	}
	cfgs, loadErr := s.dbg.Launches(s.workspaceDir, global, activeFile)
	if loadErr != nil {
		if err := res.SetError(loadErr.Error()); err != nil {
			return err
		}
	}
	list, err := res.NewConfigs(int32(len(cfgs)))
	if err != nil {
		return err
	}
	for i, c := range cfgs {
		if err := writeDebugConfig(list.At(i), c); err != nil {
			return err
		}
	}
	return nil
}

func (s *editorService) ListProcesses(_ context.Context, call proto.EditorService_listProcesses) error {
	call.Go() // reads build information from every executable the first time
	res, err := call.AllocResults()
	if err != nil {
		return err
	}
	procs, err := debug.ListProcesses()
	if err != nil {
		return res.SetError(err.Error())
	}
	list, err := res.NewProcesses(int32(len(procs)))
	if err != nil {
		return err
	}
	for i, p := range procs {
		item := list.At(i)
		item.SetPid(int64(p.PID))
		item.SetPpid(int64(p.PPID))
		for _, set := range []func() error{
			func() error { return item.SetName(p.Name) },
			func() error { return item.SetExe(p.Exe) },
			func() error { return item.SetGoVersion(p.GoVersion) },
			func() error { return item.SetGoModule(p.GoModule) },
			func() error { return writeTextList(p.Args, item.NewArgs) },
		} {
			if err := set(); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *editorService) DebugRestart(_ context.Context, call proto.EditorService_debugRestart) error {
	var fallback debug.Config
	if pc, err := call.Args().Fallback(); err == nil {
		fallback = readDebugConfig(pc)
	}
	call.Go() // stops a session and builds the program again
	res, err := call.AllocResults()
	if err != nil {
		return err
	}
	if s.dbg == nil {
		return res.SetError("debugging is not available")
	}
	if fallback.Program != "" {
		fallback = s.withDebugDefaults(fallback)
	}
	started, startErr := s.dbg.Restart(fallback)
	if ps, err := res.NewStarted(); err == nil {
		writeDebugConfig(ps, started) //nolint:errcheck // informational
	}
	if startErr != nil {
		return res.SetError(startErr.Error())
	}
	return nil
}

func (s *editorService) DebugControl(_ context.Context, call proto.EditorService_debugControl) error {
	action := call.Args().Action()
	call.Go()
	res, err := call.AllocResults()
	if err != nil {
		return err
	}
	if s.dbg == nil {
		return res.SetError(debug.ErrNoSession.Error())
	}
	a, ok := map[proto.DebugAction]debug.Action{
		proto.DebugAction_continue: debug.ActionContinue,
		proto.DebugAction_next:     debug.ActionNext,
		proto.DebugAction_stepIn:   debug.ActionStepIn,
		proto.DebugAction_stepOut:  debug.ActionStepOut,
		proto.DebugAction_pause:    debug.ActionPause,
		proto.DebugAction_stop:     debug.ActionStop,
	}[action]
	if !ok {
		return res.SetError("unknown debug action")
	}
	if err := s.dbg.Control(a); err != nil {
		return res.SetError(err.Error())
	}
	return nil
}

func (s *editorService) DebugState(_ context.Context, call proto.EditorService_debugState) error {
	res, err := call.AllocResults()
	if err != nil {
		return err
	}
	ps, err := res.NewState()
	if err != nil || s.dbg == nil {
		return err
	}
	st, seq := s.dbg.State()
	ps.SetStatus(map[debug.Status]proto.DebugStatus{
		debug.StatusInactive:   proto.DebugStatus_inactive,
		debug.StatusStarting:   proto.DebugStatus_starting,
		debug.StatusRunning:    proto.DebugStatus_running,
		debug.StatusStopped:    proto.DebugStatus_stopped,
		debug.StatusTerminated: proto.DebugStatus_terminated,
	}[st.Status])
	ps.SetSeq(seq)
	ps.SetThreadId(int64(st.ThreadID))
	ps.SetPath(st.Path) //nolint:errcheck
	ps.SetLine(uint32(max(st.Line, 0)))
	ps.SetReason(st.Reason)           //nolint:errcheck
	ps.SetDescription(st.Description) //nolint:errcheck
	ps.SetExitCode(int32(st.ExitCode))
	ps.SetHasExitCode(st.HasExitCode)
	return ps.SetError(st.Error)
}

func (s *editorService) DebugStackTrace(_ context.Context, call proto.EditorService_debugStackTrace) error {
	threadID := int(call.Args().ThreadId())
	call.Go()
	res, err := call.AllocResults()
	if err != nil {
		return err
	}
	if s.dbg == nil {
		return res.SetError(debug.ErrNoSession.Error())
	}
	frames, err := s.dbg.StackTrace(threadID)
	if err != nil {
		return res.SetError(err.Error())
	}
	list, err := res.NewFrames(int32(len(frames)))
	if err != nil {
		return err
	}
	for i, f := range frames {
		item := list.At(i)
		item.SetId(int64(f.ID))
		item.SetName(f.Name) //nolint:errcheck
		item.SetPath(f.Path) //nolint:errcheck
		item.SetLine(uint32(max(f.Line, 0)))
		item.SetCol(uint32(max(f.Col, 0)))
	}
	return nil
}

func (s *editorService) DebugScopes(_ context.Context, call proto.EditorService_debugScopes) error {
	frameID := int(call.Args().FrameId())
	call.Go()
	res, err := call.AllocResults()
	if err != nil {
		return err
	}
	if s.dbg == nil {
		return res.SetError(debug.ErrNoSession.Error())
	}
	scopes, err := s.dbg.Scopes(frameID)
	if err != nil {
		return res.SetError(err.Error())
	}
	list, err := res.NewScopes(int32(len(scopes)))
	if err != nil {
		return err
	}
	for i, sc := range scopes {
		item := list.At(i)
		item.SetName(sc.Name) //nolint:errcheck
		item.SetRef(int64(sc.Ref))
		item.SetExpensive(sc.Expensive)
	}
	return nil
}

func (s *editorService) DebugVariables(_ context.Context, call proto.EditorService_debugVariables) error {
	ref := int(call.Args().Ref())
	call.Go()
	res, err := call.AllocResults()
	if err != nil {
		return err
	}
	if s.dbg == nil {
		return res.SetError(debug.ErrNoSession.Error())
	}
	vars, err := s.dbg.Variables(ref)
	if err != nil {
		return res.SetError(err.Error())
	}
	list, err := res.NewVariables(int32(len(vars)))
	if err != nil {
		return err
	}
	for i, v := range vars {
		setDebugVariable(list.At(i), v)
	}
	return nil
}

func setDebugVariable(item proto.DebugVariable, v debug.Variable) {
	item.SetName(v.Name)   //nolint:errcheck
	item.SetValue(v.Value) //nolint:errcheck
	item.SetType(v.Type)   //nolint:errcheck
	item.SetRef(int64(v.Ref))
}

func (s *editorService) DebugEvaluate(_ context.Context, call proto.EditorService_debugEvaluate) error {
	args := call.Args()
	expr, err := args.Expression()
	if err != nil {
		return err
	}
	evalContext, _ := args.Context()
	if evalContext == "" {
		evalContext = dap.EvalREPL
	}
	frameID := int(args.FrameId())
	call.Go()
	res, err := call.AllocResults()
	if err != nil {
		return err
	}
	if s.dbg == nil {
		return res.SetError(debug.ErrNoSession.Error())
	}
	v, err := s.dbg.Evaluate(expr, frameID, evalContext)
	if err != nil {
		return res.SetError(err.Error())
	}
	item, err := res.NewResult()
	if err != nil {
		return err
	}
	setDebugVariable(item, v)
	return nil
}

func (s *editorService) DebugOutput(_ context.Context, call proto.EditorService_debugOutput) error {
	since := call.Args().SinceSeq()
	res, err := call.AllocResults()
	if err != nil || s.dbg == nil {
		return err
	}
	chunks, latest, truncated := s.dbg.Output(since)
	res.SetLatest(latest)
	res.SetTruncated(truncated)
	list, err := res.NewChunks(int32(len(chunks)))
	if err != nil {
		return err
	}
	for i, c := range chunks {
		item := list.At(i)
		item.SetSeq(c.Seq)
		item.SetCategory(c.Category) //nolint:errcheck
		item.SetText(c.Text)         //nolint:errcheck
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
