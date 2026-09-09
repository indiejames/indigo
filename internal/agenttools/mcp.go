package agenttools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/indiejames/indigo/internal/client"
	"github.com/indiejames/indigo/internal/server"
)

// ─── MCP server mode ─────────────────────────────────────────────────────────
//
// The claude CLI spawns this process and speaks MCP (JSON-RPC 2.0, one
// message per line) over stdio. Tool calls execute against live editor
// buffers, so reads see unsaved changes and edits land as undoable buffer
// ops rather than blind disk writes.
//
// There are two ways to reach those buffers:
//
//   - runMCPServer (--mcp <socket>): forwards each call to a running
//     indigo-claude TUI, which owns the connection and shows the approval
//     popup. This is what the TUI itself wires up for its own claude
//     subprocess.
//
//   - runMCPStandalone (--mcp-standalone): connects straight to the
//     workspace's indigo server, starting one if none is running. No TUI is
//     needed, which is the point — the chat UI and the editor integration
//     are separate things, and requiring the former to get the latter meant
//     that anyone not using the chat UI got nothing.

const mcpMaxLine = 4 * 1024 * 1024

// mcpServer handles decoded MCP messages. callTool is injected so tests can
// stub the socket round-trip.
type mcpServer struct {
	callTool func(name string, input json.RawMessage) (result string, isError bool)
}

// RunForwarding speaks MCP over stdio and forwards each tool call to a
// running indigo-claude TUI over its Unix socket, which owns the connection
// and shows the approval popup.
func RunForwarding(socketPath string) {
	serveMCPStdio(&mcpServer{
		callTool: func(name string, input json.RawMessage) (string, bool) {
			return forwardToolCall(socketPath, name, input)
		},
	})
}

// serveMCPStdio runs the MCP read/dispatch/write loop over stdio until stdin
// closes. Shared by both modes so they can only differ in how a tool call is
// executed, never in how the protocol is spoken.
func serveMCPStdio(srv *mcpServer) {
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 64*1024), mcpMaxLine)
	out := bufio.NewWriter(os.Stdout)
	for in.Scan() {
		line := strings.TrimSpace(in.Text())
		if line == "" {
			continue
		}
		if resp := srv.handleMessage([]byte(line)); resp != nil {
			out.Write(resp)     //nolint:errcheck
			out.WriteByte('\n') //nolint:errcheck
			out.Flush()         //nolint:errcheck
		}
	}
}

// mcpToolTimeout bounds one tool call. Generous because an edit can wait on
// the server's own format-on-save, but finite so a wedged server surfaces as
// a tool error rather than hanging the agent indefinitely.
const mcpToolTimeout = 60 * time.Second

// runMCPStandalone speaks MCP over stdio and executes each tool call directly
// against the workspace's indigo server, with no TUI in the loop.
//
// The workspace is resolved from the cwd exactly the way indigo resolves it
// (nearest .git, else the directory itself), so a single user-level
// registration works from any repo and from any subdirectory of one. If no
// server is running for that workspace, one is started — indigo's server is
// designed for this, coming up on first client connection and exiting when
// the last client disconnects, so an agent session can use the editor's
// buffers and language servers without the user having indigo open.
// RunStandalone speaks MCP over stdio and executes each tool call directly
// against the workspace's indigo server, with no chat TUI in the loop.
func RunStandalone() {
	serveMCPStdio(&mcpServer{callTool: workspaceToolCaller()})
}

// workspaceToolCaller builds the tool-execution function both transports use:
// resolve the workspace from the cwd, keep a live connection to its indigo
// server, and run each call against it.
//
// Shared by stdio and HTTP deliberately. The transport decides how bytes move;
// nothing about which tools exist, how the workspace is found, or how a stale
// server is reported may differ between them, or the two would drift into
// answering the same question differently.
//
// Exits the process on a workspace that cannot host a server at all, so that
// failure is visible at startup rather than once per tool call.
func workspaceToolCaller() func(string, json.RawMessage) (string, bool) {
	cwd, err := os.Getwd()
	if err != nil {
		mcpFatal("cannot determine working directory: %v", err)
	}
	workDir := workspaceRoot(cwd)

	conn := &mcpConn{workDir: workDir, sock: server.SocketPath(workDir)}
	if _, err := conn.get(); err != nil {
		mcpFatal("%v", err)
	}
	ap := standaloneApprover()

	// One tool call at a time, matching what stdio does structurally (it reads
	// and dispatches on a single goroutine). HTTP would otherwise let a client
	// overlap calls, and two concurrent apply_edits on one buffer is a
	// behaviour no transport has ever had here — not a difference worth
	// introducing as a side effect of adding one.
	var mu sync.Mutex

	return func(name string, input json.RawMessage) (string, bool) {
		mu.Lock()
		defer mu.Unlock()

		// Reconnect per call rather than capturing one handle for the life of
		// the process. This process outlives any editor window, and an indigo
		// server exits when its last client disconnects, so the connection made
		// at startup routinely dies mid-session. A dead handle answers every
		// later call with "rpc: connection closed" — which surfaced as edits
		// silently falling back to filesystem tools while reads appeared to
		// keep working.
		rpc, err := conn.get()
		if err != nil {
			return err.Error(), true
		}
		ctx, cancel := context.WithTimeout(context.Background(), mcpToolTimeout)
		defer cancel()
		out, isErr := ExecTool(ctx, rpc, ap, workDir, name, input)
		// A stale server is why three separate investigations in one session
		// chased phantom bugs: the tools answered normally while the server ran
		// code from before the last build. Prefixing every result is
		// deliberately heavy-handed — a warning shown once at startup is
		// exactly the kind an agent reads past and then reasons from stale
		// output anyway. Re-read per call rather than captured once, since a
		// reconnect can land on a different server than the previous call.
		if rpc.ServerStale() {
			out = staleServerWarning + out
		}
		return out, isErr
	}
}

// mcpConn hands out a live connection to the workspace's indigo server,
// redialing (and restarting the server) whenever the previous one has gone
// away.
//
// The server's lifecycle is the reason this is needed: it starts on the first
// client connection and exits when the last client disconnects. An MCP process
// registered at user scope lives across many editor sessions, so "the server I
// connected to at startup" is not a thing that stays true.
type mcpConn struct {
	mu      sync.Mutex
	workDir string
	sock    string
	rpc     *client.RPC
}

func (c *mcpConn) get() (*client.RPC, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.rpc.Alive() {
		return c.rpc, nil
	}
	c.rpc = nil

	if !server.IsRunning(c.sock) {
		if err := startIndigoServer(c.workDir); err != nil {
			return nil, fmt.Errorf("cannot start an indigo server for %s: %w", c.workDir, err)
		}
		if err := waitForIndigoServer(c.sock, 5*time.Second); err != nil {
			return nil, fmt.Errorf("indigo server for %s did not come up: %w", c.workDir, err)
		}
	}
	rpc, err := client.Dial(c.sock)
	if err != nil {
		return nil, fmt.Errorf("cannot connect to the indigo server for %s: %w", c.workDir, err)
	}
	c.rpc = rpc
	return rpc, nil
}

// standaloneProgramLink builds the programLink the standalone mode runs with.
//
// No TUI means no approval popup, so the plugin's own gate is stood down and
// approval becomes the MCP client's job — the claude CLI prompts before
// calling a tool unless the user has allowlisted it, the same model every
// other MCP server relies on.
//
// Leaving the gate enabled would not be safer, it would be broken:
// requestEditApproval emits a permission request to the TUI program and then
// blocks on the reply channel, so with no program attached every edit would
// hang until the tool timeout with no indication why.
//
// Split out as a named constructor so a test can assert this property of the
// thing standalone actually uses, rather than of a lookalike built in the
// test.
func standaloneApprover() Approver {
	return AlwaysApprove{}
}

// staleServerWarning prefixes every tool result while the connected server is
// running a replaced binary, so results can't be trusted as reflecting the
// current build.
const staleServerWarning = "WARNING: the indigo server for this workspace is running an older " +
	"build than what is installed, so this result may not reflect recent changes to indigo " +
	"itself. Close every indigo window on this workspace (or kill its `indigo --server` " +
	"process) and retry before concluding anything about indigo's own behaviour.\n\n"

// mcpFatal reports a startup failure on stderr and exits. stdout is the MCP
// transport, so nothing but protocol may ever be written there.
func mcpFatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "indigo mcp: "+format+"\n", args...)
	os.Exit(1)
}

// workspaceRoot returns the nearest ancestor containing .git, or dir itself.
// Mirrors cmd/indigo's gitRoot so both agree on which server owns a path.
func workspaceRoot(dir string) string {
	// Resolve symlinks before walking, matching cmd/indigo's resolvePath +
	// gitRoot ordering. Two things depend on agreeing with it exactly:
	//
	//   - server.SocketPath is derived from this string, so a symlinked
	//     checkout reached by two spellings would hash to two sockets and the
	//     agent would silently get a second server for the same repo, with its
	//     own buffers and its own language servers.
	//   - the server is spawned with this as its workDir, and the OS resolves a
	//     child process's cwd whether or not Go did. An unresolved workDir
	//     therefore disagrees with the cwd a linter or formatter reports for
	//     itself, and ESLint in particular then rejects the file as outside its
	//     base path.
	//
	// Resolving first also means the walk's ancestors are already resolved, so
	// the root this returns is too.
	start := resolveSymlinks(dir)
	d := start
	for {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			return start
		}
		d = parent
	}
}

// resolveSymlinks mirrors cmd/indigo's resolvePath: the resolved path, or the
// input unchanged when it cannot be resolved (it may not exist yet, or be
// unreadable).
func resolveSymlinks(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return path
}

// startIndigoServer spawns `indigo --server <workDir>`, detached, the same way
// the editor's own launcher does.
func startIndigoServer(workDir string) error {
	exe, err := exec.LookPath("indigo")
	if err != nil {
		return fmt.Errorf("indigo not found on PATH: %w", err)
	}
	cmd := exec.Command(exe, "--server", workDir)
	cmd.Dir = workDir
	// Detach: this process exits when the agent session ends, and the server
	// must outlive it to serve the next one.
	cmd.SysProcAttr = detachedSysProcAttr()
	return cmd.Start()
}

func waitForIndigoServer(sockPath string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if server.IsRunning(sockPath) {
			return nil
		}
		time.Sleep(5 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for %s", sockPath)
}

type mcpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// handleMessage processes one JSON-RPC message and returns the response bytes,
// or nil when no response is owed — which is only ever a valid notification.
func (s *mcpServer) handleMessage(raw []byte) []byte {
	// Three outcomes, and they must stay distinguishable, because a nil return
	// means "no reply is owed" — true only of a valid notification. Collapsing
	// any of these into nil was how malformed input became a 202 Accepted over
	// HTTP, telling a client its request was fine when it never parsed.
	//
	//   not JSON at all          -> -32700 parse error
	//   JSON, but not a request  -> -32600 invalid request
	//   a valid notification     -> nil
	//
	// The distinction between the first two is why this decodes twice: whether
	// the bytes are JSON is a different question from whether that JSON is a
	// request object, and reporting "parse error" for well-formed JSON sends a
	// client looking for a syntax problem that does not exist.
	if !json.Valid(raw) {
		return mcpError(nil, -32700, "parse error: not valid JSON")
	}
	var req mcpRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		// Valid JSON of the wrong shape: an array (a batch, which this server
		// does not implement, or an empty one), a bare scalar, or a member of
		// the wrong type such as a numeric "method".
		return mcpError(nil, -32600, "invalid request: not a JSON-RPC request object")
	}
	if req.JSONRPC != "2.0" {
		return mcpError(nil, -32600, `invalid request: "jsonrpc" must be "2.0"`)
	}
	if req.Method == "" {
		return mcpError(nil, -32600, `invalid request: "method" is required`)
	}
	if !validRequestID(req.ID) {
		return mcpError(nil, -32600, `invalid request: "id" must be a string, number, or null`)
	}
	// Note every invalid-request reply above carries a null id, including the
	// ones where an id was present and readable. That is JSON-RPC's rule: an id
	// is only echoed once the request it came from is known to be well formed.
	isNotification := len(req.ID) == 0 || string(req.ID) == "null"

	var result any
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		json.Unmarshal(req.Params, &p) //nolint:errcheck
		result = map[string]any{
			"protocolVersion": negotiateProtocolVersion(p.ProtocolVersion),
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "indigo", "version": "0.1.0"},
		}

	case "ping":
		result = map[string]any{}

	case "tools/list":
		result = map[string]any{"tools": mcpTools()}

	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return mcpError(req.ID, -32602, "invalid params: "+err.Error())
		}
		text, isErr := s.callTool(p.Name, p.Arguments)
		result = map[string]any{
			"content": []map[string]any{{"type": "text", "text": text}},
			"isError": isErr,
		}

	default:
		if isNotification {
			return nil
		}
		return mcpError(req.ID, -32601, "method not found: "+req.Method)
	}

	if isNotification {
		return nil
	}
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	return b
}

// validRequestID reports whether an id is one JSON-RPC permits: a string, a
// number, or null. An absent id is fine and means a notification — the one
// thing this must not reject, since notifications are the reason a nil
// response exists at all.
//
// Checking the first byte is enough: json.Valid has already run, so the value
// is well-formed and only its type is in question.
func validRequestID(id json.RawMessage) bool {
	trimmed := bytes.TrimSpace(id)
	if len(trimmed) == 0 {
		return true // absent: a notification
	}
	switch c := trimmed[0]; {
	case c == '"', c == '-', c >= '0' && c <= '9':
		return true
	default:
		return bytes.Equal(trimmed, []byte("null"))
	}
}

func mcpError(id json.RawMessage, code int, msg string) []byte {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	b, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   map[string]any{"code": code, "message": msg},
	})
	return b
}

// mcpTool mirrors ToolDef but with the camelCase inputSchema key MCP expects,
// plus the annotations MCP uses to describe a tool's effects.
type mcpTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema ToolSchema      `json:"inputSchema"`
	Annotations *mcpAnnotations `json:"annotations,omitempty"`
}

// mcpAnnotations tells the client what a tool does to the world. It matters
// most for the standalone mode, where the client's own prompt is the only
// approval gate: marking the query tools read-only lets a user allowlist them
// without also allowlisting the ones that modify buffers and write files.
type mcpAnnotations struct {
	ReadOnlyHint    bool `json:"readOnlyHint"`
	DestructiveHint bool `json:"destructiveHint"`
}

// readOnlyTools never modify a buffer or the filesystem: they open a file to
// query it and close it again. Everything not listed here can change the
// user's code and must keep its prompt.
var readOnlyTools = map[string]bool{
	"read_file":                 true,
	"get_diagnostics":           true,
	"get_workspace_diagnostics": true,
	"find_definition":           true,
	"find_references":           true,
	"list_symbols":              true,
}

// mcpTools exposes the buffer-aware file tools, the language-server query
// tools, and goto_file (no native equivalent — it drives the indigo editor UI,
// not the filesystem).
//
// list_files/search_files are omitted: claude's native Glob/Grep cover those
// and disk-based search has no buffer-consistency problem. The LSP tools are
// the opposite case — there is no native equivalent at all, and they are the
// main reason to attach indigo to an agent session.
func mcpTools() []mcpTool {
	exposed := map[string]bool{
		"read_file": true, "apply_edits": true, "insert_at_line": true,
		"save_file": true, "goto_file": true,
		"get_diagnostics": true, "get_workspace_diagnostics": true,
		"find_definition": true,
		"find_references": true, "list_symbols": true,
	}
	var out []mcpTool
	for _, t := range AllTools() {
		if !exposed[t.Name] {
			continue
		}
		ro := readOnlyTools[t.Name]
		out = append(out, mcpTool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
			Annotations: &mcpAnnotations{
				ReadOnlyHint: ro,
				// Nothing here deletes or truncates; an edit is undoable in
				// the editor and, for save_file, reversible from git.
				DestructiveHint: false,
			},
		})
	}
	return out
}

// forwardToolCall sends one tool call to the TUI's Unix socket and reads the
// single-line JSON reply. One connection per call keeps concurrency trivial.
func forwardToolCall(socketPath, name string, input json.RawMessage) (string, bool) {
	// Bounded at every step. The TUI on the other end blocks on a human for an
	// edit approval, so a slow reply is normal and the budget is generous — but
	// none of dial, write or read may block forever, or a TUI that has wedged
	// (or died between the socket existing and being served) hangs the agent
	// with no error to act on. mcpToolTimeout is the same budget the standalone
	// path gives one tool call.
	conn, err := net.DialTimeout("unix", socketPath, mcpToolTimeout)
	if err != nil {
		return "indigo-claude TUI not reachable: " + err.Error(), true
	}
	defer conn.Close() //nolint:errcheck
	// One deadline covers the write and the read together: the call as a whole
	// is what has a budget, not each syscall separately.
	if err := conn.SetDeadline(time.Now().Add(mcpToolTimeout)); err != nil {
		return "cannot set a deadline on the tool call: " + err.Error(), true
	}

	req, _ := json.Marshal(map[string]any{"type": "mcp_tool_call", "name": name, "input": input})
	if _, err := conn.Write(append(req, '\n')); err != nil {
		return "cannot send tool call: " + err.Error(), true
	}

	line, err := bufio.NewReaderSize(conn, 64*1024).ReadString('\n')
	if err != nil {
		return "no reply from indigo-claude: " + err.Error(), true
	}
	var resp struct {
		Result  string `json:"result"`
		IsError bool   `json:"is_error"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &resp); err != nil {
		return "bad reply from indigo-claude: " + err.Error(), true
	}
	return resp.Result, resp.IsError
}

// writeMCPConfig writes the --mcp-config file handed to the claude subprocess:
// it tells claude to spawn this same binary in --mcp mode, pointed at the
// TUI's tool socket.
// WriteMCPConfig writes the --mcp-config file the claude subprocess uses to
// reach this process's tool socket.
func WriteMCPConfig(path, binaryPath, socketPath string) error {
	cfg := map[string]any{
		"mcpServers": map[string]any{
			"indigo": map[string]any{
				"type":    "stdio",
				"command": binaryPath,
				"args":    []string{"--mcp", socketPath},
			},
		},
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0600)
}

// supportedProtocolVersions are the MCP revisions this server implements, in
// preference order. The first entry is what an initialize with no version, or
// with one we do not implement, is answered with.
//
// Only the subset of MCP used here matters — initialize, tools/list,
// tools/call, ping, and the notification shapes around them — and it is
// identical across these revisions, which is why more than one can be claimed
// honestly.
var supportedProtocolVersions = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

// negotiateProtocolVersion picks the revision to report back from initialize.
//
// It used to echo whatever the client asked for, which claims support for any
// revision a client cares to name — including future ones with semantics this
// server does not implement. The spec's own resolution for a version the server
// does not support is to answer with one it does and let the client decide
// whether to proceed, which is what this does.
func negotiateProtocolVersion(requested string) string {
	for _, v := range supportedProtocolVersions {
		if requested == v {
			return v
		}
	}
	return supportedProtocolVersions[0]
}
