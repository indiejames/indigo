package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/indiejames/indigo/internal/client"
)

// ─── hook client mode ─────────────────────────────────────────────────────────

// runHookClient is invoked when indigo-claude is called with --hook <socket>.
// It reads PreToolUse JSON from stdin, forwards it to the running indigo-claude
// TUI via the Unix socket, and prints the decision JSON to stdout for claude.
func runHookClient(socketPath string) {
	var input strings.Builder
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		input.WriteString(scanner.Text())
	}

	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		// indigo-claude not reachable — allow so claude isn't blocked.
		os.Stdout.Write(decisionJSON(true)) //nolint:errcheck
		return
	}
	defer conn.Close() //nolint:errcheck

	if _, err := fmt.Fprintln(conn, input.String()); err != nil {
		os.Stdout.Write(decisionJSON(true)) //nolint:errcheck
		return
	}

	resp, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		os.Stdout.Write(decisionJSON(true)) //nolint:errcheck
		return
	}
	fmt.Print(strings.TrimSpace(resp))
}

// ─── decision JSON ────────────────────────────────────────────────────────────

type hookDecision struct {
	HookSpecificOutput hookOutput `json:"hookSpecificOutput"`
}

type hookOutput struct {
	HookEventName            string `json:"hookEventName"`
	PermissionDecision       string `json:"permissionDecision"`
	PermissionDecisionReason string `json:"permissionDecisionReason,omitempty"`
}

func decisionJSON(approved bool) []byte {
	decision, reason := "allow", ""
	if !approved {
		decision = "deny"
		reason = "User rejected the command"
	}
	b, _ := json.Marshal(hookDecision{HookSpecificOutput: hookOutput{
		HookEventName:            "PreToolUse",
		PermissionDecision:       decision,
		PermissionDecisionReason: reason,
	}})
	return b
}

// ─── permission / tool socket server ──────────────────────────────────────────

// startPermissionServer listens on socketPath and routes incoming requests to
// the TUI: PreToolUse hook events become shellPermissionRequestMsg values, and
// mcp_tool_call requests (from the --mcp subprocess) execute buffer-aware
// tools via execTool. Returns the listener so the caller can close it on exit.
func startPermissionServer(socketPath string, prog *programLink, rpc *client.RPC, workDir string) (net.Listener, error) {
	os.Remove(socketPath) //nolint:errcheck
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("permission socket: %w", err)
	}
	// Owner-only, independent of umask. The parent directory is already 0700;
	// this is defense in depth for platforms that enforce socket file modes.
	os.Chmod(socketPath, 0600) //nolint:errcheck
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go handlePermConn(conn, prog, rpc, workDir)
		}
	}()
	return ln, nil
}

func handlePermConn(conn net.Conn, prog *programLink, rpc *client.RPC, workDir string) {
	defer conn.Close() //nolint:errcheck

	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		conn.Write(append(decisionJSON(true), '\n')) //nolint:errcheck
		return
	}
	trimmed := strings.TrimSpace(line)

	// MCP tool calls share this socket with hook events; they carry an explicit
	// type marker while hook JSON has tool_name/tool_input.
	var probe struct {
		Type string `json:"type"`
	}
	json.Unmarshal([]byte(trimmed), &probe) //nolint:errcheck
	if probe.Type == "mcp_tool_call" {
		handleMCPToolCall(conn, trimmed, prog, rpc, workDir)
		return
	}

	var ev struct {
		ToolName  string `json:"tool_name"`
		ToolInput struct {
			Command string `json:"command"`
		} `json:"tool_input"`
	}
	if err := json.Unmarshal([]byte(trimmed), &ev); err != nil {
		conn.Write(append(decisionJSON(true), '\n')) //nolint:errcheck
		return
	}

	replyCh := make(chan bool, 1)
	prog.emit(shellPermissionRequestMsg{
		command: ev.ToolInput.Command,
		replyCh: replyCh,
	})

	approved := <-replyCh
	conn.Write(append(decisionJSON(approved), '\n')) //nolint:errcheck
}

// handleMCPToolCall executes one forwarded MCP tool call against the live
// editor buffers and writes the single-line JSON reply. apply_edits blocks on
// the in-editor approval popup, so this can wait on the user.
func handleMCPToolCall(conn net.Conn, line string, prog *programLink, rpc *client.RPC, workDir string) {
	writeReply := func(result string, isError bool) {
		b, _ := json.Marshal(map[string]any{"result": result, "is_error": isError})
		conn.Write(append(b, '\n')) //nolint:errcheck
	}

	var req struct {
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal([]byte(line), &req); err != nil {
		writeReply("bad tool call request: "+err.Error(), true)
		return
	}
	result, isError := execTool(context.Background(), rpc, prog, workDir, req.Name, req.Input)
	writeReply(result, isError)
}

// ─── hook script + settings ───────────────────────────────────────────────────

func writeHookScript(scriptPath, binaryPath, socketPath string) error {
	// The env guard scopes the hook to the claude subprocess spawned by
	// indigo-claude (which sets INDIGO_CLAUDE_HOOK=1). Any other Claude Code
	// session in this workspace also runs the hook — settings.local.json
	// applies directory-wide — but exits silently with no decision, so those
	// sessions keep their own permission flow instead of popping dialogs in
	// the indigo-claude TUI.
	content := fmt.Sprintf("#!/bin/sh\n[ \"$INDIGO_CLAUDE_HOOK\" = \"1\" ] || exit 0\nexec %s --hook %s\n", binaryPath, socketPath)
	return os.WriteFile(scriptPath, []byte(content), 0700)
}

// installHook merges a PreToolUse/Bash hook entry into the project-local
// .claude/settings.local.json (gitignored, does not affect committed settings).
func installHook(workDir, scriptPath string) error {
	settingsPath := filepath.Join(workDir, ".claude", "settings.local.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0755); err != nil {
		return err
	}

	settings := map[string]any{}
	if raw, err := os.ReadFile(settingsPath); err == nil {
		json.Unmarshal(raw, &settings) //nolint:errcheck
	}

	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
		settings["hooks"] = hooks
	}
	existing, _ := hooks["PreToolUse"].([]any)

	// Remove any stale indigo-claude entries before adding the current one.
	var kept []any
	for _, e := range existing {
		if !isOurHookEntry(e) {
			kept = append(kept, e)
		}
	}
	hooks["PreToolUse"] = append(kept, map[string]any{
		"matcher": "Bash",
		"hooks": []any{map[string]any{
			"type":    "command",
			"command": scriptPath,
			"timeout": 60,
		}},
	})

	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(settingsPath, data, 0644)
}

// removeHook strips our hook entry from .claude/settings.local.json on exit.
func removeHook(workDir string) {
	settingsPath := filepath.Join(workDir, ".claude", "settings.local.json")
	raw, err := os.ReadFile(settingsPath)
	if err != nil {
		return
	}
	settings := map[string]any{}
	if err := json.Unmarshal(raw, &settings); err != nil {
		return
	}

	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		return
	}
	existing, _ := hooks["PreToolUse"].([]any)

	var kept []any
	for _, e := range existing {
		if !isOurHookEntry(e) {
			kept = append(kept, e)
		}
	}
	if len(kept) == 0 {
		delete(hooks, "PreToolUse")
	} else {
		hooks["PreToolUse"] = kept
	}
	if len(hooks) == 0 {
		delete(settings, "hooks")
	}
	if len(settings) == 0 {
		os.Remove(settingsPath) //nolint:errcheck
		return
	}
	if data, err := json.MarshalIndent(settings, "", "  "); err == nil {
		os.WriteFile(settingsPath, data, 0644) //nolint:errcheck
	}
}

// isOurHookEntry identifies entries written by indigo-claude by their command
// path, which always contains "indigo-claude-hook" (also matches the older
// "indigo-claude-hook-<pid>.sh" naming so stale entries still get cleaned up).
func isOurHookEntry(entry any) bool {
	e, ok := entry.(map[string]any)
	if !ok {
		return false
	}
	hs, _ := e["hooks"].([]any)
	for _, h := range hs {
		if hm, ok := h.(map[string]any); ok {
			if strings.Contains(fmt.Sprint(hm["command"]), "indigo-claude-hook") {
				return true
			}
		}
	}
	return false
}
