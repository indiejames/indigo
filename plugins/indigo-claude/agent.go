package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/indiejames/indigo/internal/client"
)

// ─── program link ────────────────────────────────────────────────────────────

// programLink lets goroutines push tea.Msg values to the running program.
// The send field is set after tea.NewProgram returns its *Program.
type programLink struct {
	mu     sync.Mutex
	send   func(tea.Msg)
	cancel context.CancelFunc

	// mcpConfig is the path of the --mcp-config file for the claude subprocess.
	// Set once in main() before the program starts; empty when the MCP bridge
	// could not be set up (subprocess then falls back to built-in file tools).
	mcpConfig string
}

func (pl *programLink) emit(msg tea.Msg) {
	pl.mu.Lock()
	fn := pl.send
	pl.mu.Unlock()
	if fn != nil {
		fn(msg)
	}
}

func (pl *programLink) setCancel(fn context.CancelFunc) {
	pl.mu.Lock()
	pl.cancel = fn
	pl.mu.Unlock()
}

func (pl *programLink) cancelAgent() {
	pl.mu.Lock()
	fn := pl.cancel
	pl.cancel = nil
	pl.mu.Unlock()
	if fn != nil {
		fn()
	}
}

// ─── agent tea messages ──────────────────────────────────────────────────────

type agentTextDeltaMsg struct{ text string }
type agentToolStartMsg struct{ name string }
type agentToolDoneMsg struct{ name string }
type agentDoneMsg struct {
	history   []apiMessage // populated by API mode
	sessionID string       // populated by CLI mode
}
type agentErrorMsg struct{ err error }

// agentUnknownEventMsg carries a raw stream-json line whose "type" field we
// don't recognise yet. Shown in the TUI so we can discover new event formats
// (e.g. permission requests).
type agentUnknownEventMsg struct{ raw string }

// shellPermissionRequestMsg asks the TUI to show a shell command and get user
// approval. The hook goroutine blocks on replyCh until the user responds.
type shellPermissionRequestMsg struct {
	command string
	replyCh chan bool
}

// permissionRequestMsg asks the TUI to show a diff and get user approval.
// The agent goroutine blocks on replyCh until the user responds.
type permissionRequestMsg struct {
	file    string
	reason  string
	edits   []editSpec
	replyCh chan bool
}

// ─── buffer context snippet ──────────────────────────────────────────────────

// bufferSnippet reads ±radius lines around line (0-indexed) from filePath and
// returns a fenced block with the cursor line marked. Returns "" on error.
func bufferSnippet(filePath string, line, radius int) string {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return ""
	}
	fileLines := strings.Split(string(data), "\n")
	start := max(0, line-radius)
	end := min(len(fileLines), line+radius+1)

	var sb strings.Builder
	sb.WriteString("```\n")
	for i := start; i < end; i++ {
		if i == line {
			fmt.Fprintf(&sb, "▶ %4d  %s\n", i+1, fileLines[i])
		} else {
			fmt.Fprintf(&sb, "  %4d  %s\n", i+1, fileLines[i])
		}
	}
	sb.WriteString("```")
	return sb.String()
}

// ─── API mode: agentic loop ──────────────────────────────────────────────────

func buildSystemPrompt(workDir string, ac client.ActiveContext) string {
	var sb strings.Builder
	sb.WriteString("You are an AI coding assistant integrated into the Indigo terminal editor.\n\n")
	sb.WriteString("Workspace root: " + workDir + "\n")
	if ac.Found {
		fmt.Fprintf(&sb, "Active file: %s (cursor at line %d, col %d)\n",
			ac.FilePath, ac.Line+1, ac.Col+1)
	}
	sb.WriteString(`
You have tools to read files, search the codebase, list directories, and apply edits.
Use them liberally — don't make assumptions about code you haven't read.

Rules:
- Always read a file before editing it.
- Prefer small, focused edits over large rewrites.
- Explain your reasoning before using apply_edits.
- Each apply_edits call requires user approval; batch related edits into one call when possible.
- When the user asks a question that doesn't require editing, just answer.
- This is a text-only terminal interface. The user cannot share screenshots, images, or any visual media.
  If you need visual information, use your tools to read the relevant source code directly. Never ask for a screenshot.
`)
	return sb.String()
}

func buildUserMessage(text string, ac client.ActiveContext, snippet string) apiMessage {
	if !ac.Found {
		return userText(text)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "[Active file: %s, line %d]\n\n", ac.FilePath, ac.Line+1)
	if snippet != "" {
		sb.WriteString("Context around cursor:\n")
		sb.WriteString(snippet)
		sb.WriteString("\n\n")
	}
	sb.WriteString(text)
	return userText(sb.String())
}

// runAgent runs the direct-API agentic loop in a goroutine.
func runAgent(prog *programLink, rpc *client.RPC, apiKey, workDir string, history []apiMessage, ac client.ActiveContext, snippet string) {
	ctx, cancel := context.WithCancel(context.Background())
	prog.setCancel(cancel)
	defer prog.setCancel(nil)

	system := buildSystemPrompt(workDir, ac)
	tools := allTools()

	for {
		var (
			textBuf    strings.Builder
			toolCalls  []streamToolEvent
			stopReason string
		)

		err := streamAPI(ctx, apiKey, system, history, tools, func(ev any) {
			switch e := ev.(type) {
			case streamTextEvent:
				textBuf.WriteString(e.text)
				prog.emit(agentTextDeltaMsg(e))
			case streamToolEvent:
				toolCalls = append(toolCalls, e)
				prog.emit(agentToolStartMsg{name: e.name})
			case streamStopEvent:
				stopReason = e.stopReason
			}
		})
		if err != nil {
			prog.emit(agentErrorMsg{err: fmt.Errorf("API error: %w", err)})
			return
		}

		// Build assistant message for history.
		assistantBlocks := []apiBlock{}
		if textBuf.Len() > 0 {
			assistantBlocks = append(assistantBlocks, textBlock(textBuf.String()))
		}
		for _, tc := range toolCalls {
			assistantBlocks = append(assistantBlocks, toolUseBlock(tc.id, tc.name, tc.input))
		}
		if len(assistantBlocks) > 0 {
			history = append(history, apiMessage{Role: "assistant", Content: assistantBlocks})
		}

		if stopReason == "end_turn" || len(toolCalls) == 0 {
			prog.emit(agentDoneMsg{history: history})
			return
		}

		// Execute tools and collect results.
		resultBlocks := []apiBlock{}
		for _, tc := range toolCalls {
			result, isError := execTool(ctx, rpc, prog, workDir, tc.name, tc.input)
			prog.emit(agentToolDoneMsg{name: tc.name})
			content := result
			if isError {
				content = "ERROR: " + result
			}
			resultBlocks = append(resultBlocks, toolResultBlock(tc.id, content, isError))
		}
		history = append(history, apiMessage{Role: "user", Content: resultBlocks})
	}
}

// toolDisplayName returns a short human-readable label for a tool call.
func toolDisplayName(name string, input json.RawMessage) string {
	var args map[string]string
	json.Unmarshal(input, &args) //nolint:errcheck

	// Our own MCP tools: "mcp__indigo__read_file" → "read_file: main.go".
	if bare, ok := strings.CutPrefix(name, "mcp__indigo__"); ok {
		if p := args["path"]; p != "" {
			return bare + ": " + filepath.Base(p)
		}
		return bare
	}

	switch name {
	case "Read", "Edit", "Write", "MultiEdit", "NotebookEdit":
		if f := args["file_path"]; f != "" {
			return name + ": " + filepath.Base(f)
		}
	case "Bash":
		if cmd := args["command"]; cmd != "" {
			if len(cmd) > 32 {
				cmd = cmd[:32] + "…"
			}
			return "Bash: " + cmd
		}
	case "Grep":
		if p := args["pattern"]; p != "" {
			return "Grep: " + p
		}
	case "Glob":
		if p := args["pattern"]; p != "" {
			return "Glob: " + p
		}
	case "WebSearch":
		if q := args["query"]; q != "" {
			return "Search: " + q
		}
	case "WebFetch":
		if u := args["url"]; u != "" {
			return "Fetch: " + u
		}
	}
	return name
}

// ─── subprocess runner ───────────────────────────────────────────────────────

// runClaudeSubprocess runs `claude -p` as a subprocess and emits agent events.
// sessionID is empty for the first turn; subsequent turns pass --resume so Claude
// Code continues the conversation with full context.
func runClaudeSubprocess(prog *programLink, workDir, prompt, sessionID string, ac client.ActiveContext, snippet string) {
	args := []string{
		"-p",
		"--output-format", "stream-json",
		"--include-partial-messages",
		"--verbose",
		"--permission-mode", "bypassPermissions",
	}
	if sessionID != "" {
		args = append(args, "--resume", sessionID)
	}
	if prog.mcpConfig != "" {
		// Route file reads/edits through the indigo editor's live buffers.
		// Built-in disk-based file tools are disabled so reads always match
		// the buffer content that apply_edits operates on.
		args = append(args,
			"--mcp-config", prog.mcpConfig,
			"--disallowedTools", "Read,Edit,Write,MultiEdit,NotebookEdit",
		)
	}
	sysPrompt := "This is indigo-claude, a text-only terminal interface. " +
		"The user cannot share screenshots, images, or any visual media. " +
		"If you need visual information, use your file-reading tools to inspect the source code directly. " +
		"Never ask for a screenshot."
	if prog.mcpConfig != "" {
		sysPrompt += " File access goes through the indigo editor:" +
			" use mcp__indigo__read_file to read files (returns live buffer content, including unsaved edits)" +
			" and mcp__indigo__apply_edits to edit files (edits apply to the live buffer and the user sees a diff to approve)." +
			" The built-in Read, Edit, Write, MultiEdit, and NotebookEdit tools are disabled in this session;" +
			" Glob, Grep, and Bash remain available."
	}
	if ac.Found {
		sysPrompt += fmt.Sprintf(
			" The user is currently editing %s at line %d, column %d."+
				" When suggesting edits to this file, prefer editing that file first.",
			ac.FilePath, ac.Line+1, ac.Col+1,
		)
	}
	args = append(args, "--append-system-prompt", sysPrompt)

	ctx, cancel := context.WithCancel(context.Background())
	prog.setCancel(cancel)
	defer prog.setCancel(nil)

	// Prepend file context to the user prompt so Claude sees the relevant code
	// without needing a read_file tool call.
	userPrompt := prompt
	if ac.Found && snippet != "" {
		userPrompt = fmt.Sprintf("[File: %s, line %d]\nContext around cursor:\n%s\n\n%s",
			ac.FilePath, ac.Line+1, snippet, prompt)
	}

	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Dir = workDir
	// apply_edits blocks on the in-editor approval popup, so give MCP tool
	// calls a generous timeout (values in milliseconds).
	cmd.Env = append(os.Environ(),
		"MCP_TIMEOUT=30000",
		"MCP_TOOL_TIMEOUT=600000",
	)
	// strings.NewReader closes stdin (returns EOF) as soon as the prompt is
	// consumed. That's needed because claude reads stdin until EOF to get the
	// full prompt. Permission responses via stdin require a different approach
	// once we know the stream-json permission-request event format.
	cmd.Stdin = strings.NewReader(userPrompt)

	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		prog.emit(agentErrorMsg{err: fmt.Errorf("pipe: %w", err)})
		return
	}
	if err := cmd.Start(); err != nil {
		prog.emit(agentErrorMsg{err: fmt.Errorf("start claude: %w", err)})
		return
	}

	var (
		newSessionID string
		lastTextLen  int
		// Track announced tool calls by ID so partial events don't double-emit.
		announced    = map[string]bool{}
		// Map tool_use ID → display name so tool_result events can name the tool.
		pendingTools = map[string]string{}
	)

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 512*1024), 512*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var ev map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		var evType string
		json.Unmarshal(ev["type"], &evType) //nolint:errcheck

		switch evType {
		case "system":
			json.Unmarshal(ev["session_id"], &newSessionID) //nolint:errcheck

		case "assistant":
			var wrapper struct {
				Message struct {
					Content []struct {
						Type  string          `json:"type"`
						Text  string          `json:"text"`
						ID    string          `json:"id"`
						Name  string          `json:"name"`
						Input json.RawMessage `json:"input"`
					} `json:"content"`
				} `json:"message"`
			}
			if json.Unmarshal([]byte(line), &wrapper) != nil {
				continue
			}
			var fullText strings.Builder
			for _, blk := range wrapper.Message.Content {
				switch blk.Type {
				case "text":
					fullText.WriteString(blk.Text)
				case "tool_use":
					display := toolDisplayName(blk.Name, blk.Input)
					pendingTools[blk.ID] = display
					if !announced[blk.ID] {
						announced[blk.ID] = true
						prog.emit(agentToolStartMsg{name: display})
					}
				}
			}
			text := fullText.String()
			// If text shrank, a new assistant turn started after a tool call.
			if len(text) < lastTextLen {
				lastTextLen = 0
			}
			if len(text) > lastTextLen {
				prog.emit(agentTextDeltaMsg{text: text[lastTextLen:]})
				lastTextLen = len(text)
			}

		case "user":
			// Tool results arrive as user messages. Mark each pending tool done
			// and reset the text counter for the next assistant segment.
			var wrapper struct {
				Message struct {
					Content []struct {
						Type      string `json:"type"`
						ToolUseID string `json:"tool_use_id"`
					} `json:"content"`
				} `json:"message"`
			}
			if json.Unmarshal([]byte(line), &wrapper) == nil {
				for _, blk := range wrapper.Message.Content {
					if blk.Type == "tool_result" {
						name := pendingTools[blk.ToolUseID]
						if name == "" {
							name = "tool"
						}
						prog.emit(agentToolDoneMsg{name: name})
						delete(pendingTools, blk.ToolUseID)
						delete(announced, blk.ToolUseID)
					}
				}
			}
			lastTextLen = 0

		case "result":
			var result struct {
				IsError bool   `json:"is_error"`
				Result  string `json:"result"`
			}
			if json.Unmarshal([]byte(line), &result) == nil && result.IsError {
				prog.emit(agentErrorMsg{err: fmt.Errorf("%s", result.Result)})
				cmd.Wait() //nolint:errcheck
				return
			}

		default:
			// Surface unrecognised event types (e.g. permission_request) so we
			// can discover their format and handle them properly.
			if evType != "" {
				prog.emit(agentUnknownEventMsg{raw: line})
			}
		}
	}

	if err := cmd.Wait(); err != nil {
		msg := strings.TrimSpace(stderrBuf.String())
		if msg == "" {
			msg = err.Error()
		}
		prog.emit(agentErrorMsg{err: fmt.Errorf("%s", msg)})
		return
	}
	prog.emit(agentDoneMsg{sessionID: newSessionID})
}
