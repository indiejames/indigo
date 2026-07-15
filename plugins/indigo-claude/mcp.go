package main

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"strings"
)

// ─── MCP server mode ─────────────────────────────────────────────────────────
//
// runMCPServer is invoked when indigo-claude is called with --mcp <socket>.
// The claude CLI spawns this process and speaks MCP (JSON-RPC 2.0, one message
// per line) over stdio. Tool calls are forwarded to the running indigo-claude
// TUI via the Unix socket, where they execute against live editor buffers —
// so reads see unsaved changes and edits land as undoable buffer ops.

const mcpMaxLine = 4 * 1024 * 1024

// mcpServer handles decoded MCP messages. callTool is injected so tests can
// stub the socket round-trip.
type mcpServer struct {
	callTool func(name string, input json.RawMessage) (result string, isError bool)
}

func runMCPServer(socketPath string) {
	srv := &mcpServer{
		callTool: func(name string, input json.RawMessage) (string, bool) {
			return forwardToolCall(socketPath, name, input)
		},
	}
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

type mcpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// handleMessage processes one JSON-RPC message and returns the response bytes,
// or nil when no response should be sent (notifications, unparseable input).
func (s *mcpServer) handleMessage(raw []byte) []byte {
	var req mcpRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil
	}
	isNotification := len(req.ID) == 0 || string(req.ID) == "null"

	var result any
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		json.Unmarshal(req.Params, &p) //nolint:errcheck
		ver := p.ProtocolVersion
		if ver == "" {
			ver = "2024-11-05"
		}
		result = map[string]any{
			"protocolVersion": ver,
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

// mcpTool mirrors toolDef but with the camelCase inputSchema key MCP expects.
type mcpTool struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	InputSchema toolSchema `json:"inputSchema"`
}

// mcpTools exposes only the buffer-aware file tools. list_files/search_files
// are omitted: claude's native Glob/Grep cover those and disk-based search has
// no buffer-consistency problem.
func mcpTools() []mcpTool {
	var out []mcpTool
	for _, t := range allTools() {
		switch t.Name {
		case "read_file", "apply_edits", "insert_at_line", "save_file":
			out = append(out, mcpTool{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema})
		}
	}
	return out
}

// forwardToolCall sends one tool call to the TUI's Unix socket and reads the
// single-line JSON reply. One connection per call keeps concurrency trivial.
func forwardToolCall(socketPath, name string, input json.RawMessage) (string, bool) {
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return "indigo-claude TUI not reachable: " + err.Error(), true
	}
	defer conn.Close() //nolint:errcheck

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
func writeMCPConfig(path, binaryPath, socketPath string) error {
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
