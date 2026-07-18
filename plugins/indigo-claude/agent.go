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
	"strconv"
	"strings"
	"sync"
	"time"

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

	// Auto-approve toggles (/autoapprove), read by tool-exec and hook
	// goroutines before showing a permission popup. Session-only — never
	// persisted — so a forgotten "on" from a previous run can't silently
	// skip approval later.
	autoApproveEdits bool
	autoApproveShell bool
}

func (pl *programLink) emit(msg tea.Msg) {
	pl.mu.Lock()
	fn := pl.send
	pl.mu.Unlock()
	if fn != nil {
		fn(msg)
	}
}

// setAutoApproveEdits sets whether file-edit tools (apply_edits,
// insert_at_line, save_file) skip the approval popup and proceed as if
// approved.
func (pl *programLink) setAutoApproveEdits(on bool) {
	pl.mu.Lock()
	pl.autoApproveEdits = on
	pl.mu.Unlock()
}

// setAutoApproveShell sets whether shell commands (via the PreToolUse hook)
// skip the approval popup and proceed as if approved.
func (pl *programLink) setAutoApproveShell(on bool) {
	pl.mu.Lock()
	pl.autoApproveShell = on
	pl.mu.Unlock()
}

// autoApprove returns the current (edits, shell) auto-approve state.
func (pl *programLink) autoApprove() (edits, shell bool) {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	return pl.autoApproveEdits, pl.autoApproveShell
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

// agentUsageMsg reports token usage. ctxTokens approximates the conversation's
// context size (input + cache + output of the latest request); costUSD is the
// incremental cost of the finished turn (CLI mode only, 0 in API mode).
type agentUsageMsg struct {
	ctxTokens int
	costUSD   float64
}

type agentErrorMsg struct {
	err error
	// friendly is a classified, human-readable explanation ("" = show raw err).
	friendly string
	// prompt is the user prompt of the failed turn so the TUI can restore it
	// into the input box for an easy retry.
	prompt string
}

// classifyAgentError maps raw error text to a friendly, actionable message.
// Returns "" when the error isn't recognised.
func classifyAgentError(msg string) string {
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "usage limit reached"):
		// Subscription limits arrive as "Claude AI usage limit reached|<epoch>".
		if i := strings.LastIndex(msg, "|"); i >= 0 {
			if epoch, err := strconv.ParseInt(strings.TrimSpace(msg[i+1:]), 10, 64); err == nil {
				return fmt.Sprintf("Usage limit reached — resets at %s. The conversation is preserved; retry after the reset.",
					time.Unix(epoch, 0).Format("15:04"))
			}
		}
		return "Usage limit reached. The conversation is preserved; retry after the limit resets."
	case strings.Contains(lower, "rate_limit") || strings.Contains(lower, "rate limit") || strings.Contains(lower, "429"):
		return "Rate limited — too many requests. Wait a moment, then retry."
	case strings.Contains(lower, "overloaded"):
		return "The API is temporarily overloaded. Wait a moment, then retry."
	case strings.Contains(lower, "credit balance"):
		return "API credit balance too low — top up your account, then retry."
	case strings.Contains(lower, "invalid api key") || strings.Contains(lower, "authentication") ||
		strings.Contains(lower, "unauthorized") || strings.Contains(lower, "401"):
		return "Authentication failed — check your API key or run `claude login`."
	case strings.Contains(lower, "context") && strings.Contains(lower, "exceed"):
		return "The conversation no longer fits the context window — use /clear to start fresh."
	case strings.Contains(lower, "no such host") || strings.Contains(lower, "connection refused") ||
		strings.Contains(lower, "dial tcp") || strings.Contains(lower, "timeout") ||
		strings.Contains(lower, "temporary failure"):
		return "Network error — check your connection, then retry."
	}
	return ""
}

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

// bufferContent returns the live editor buffer content for filePath (so
// unsaved edits are reflected), falling back to disk when the RPC is
// unavailable. Returns "" on error.
func bufferContent(rpc *client.RPC, filePath string) string {
	var content string
	if rpc != nil && filePath != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if bufID, c, _, _, err := rpc.OpenFile(ctx, filePath); err == nil {
			content = c
			if count, cerr := rpc.BufferClientCount(ctx, bufID); cerr == nil && count == 1 {
				rpc.CloseBuffer(ctx, bufID) //nolint:errcheck
			}
		}
	}
	if content == "" {
		data, err := os.ReadFile(filePath)
		if err != nil {
			return ""
		}
		content = string(data)
	}
	return content
}

// extractSelection returns the selected text from content using the editor's
// selection semantics: document-ordered range, end column inclusive, IsLine
// meaning whole lines.
func extractSelection(content string, sel client.ActiveSelection) string {
	lines := strings.Split(content, "\n")
	sl, el := int(sel.StartLine), int(sel.EndLine)
	if sl >= len(lines) {
		return ""
	}
	if el >= len(lines) {
		el = len(lines) - 1
	}
	if sel.IsLine {
		return strings.Join(lines[sl:el+1], "\n")
	}
	sc, ec := int(sel.StartCol), int(sel.EndCol)
	if sl == el {
		runes := []rune(lines[sl])
		hi := min(ec+1, len(runes))
		lo := min(sc, hi)
		return string(runes[lo:hi])
	}
	var sb strings.Builder
	first := []rune(lines[sl])
	sb.WriteString(string(first[min(sc, len(first)):]))
	sb.WriteByte('\n')
	for l := sl + 1; l < el; l++ {
		sb.WriteString(lines[l])
		sb.WriteByte('\n')
	}
	last := []rune(lines[el])
	sb.WriteString(string(last[:min(ec+1, len(last))]))
	return sb.String()
}

// selectionNote returns a prompt fragment describing the active selection
// ("Selected text (lines 10-25):\n```…```\n\n"), or "" when there is no
// selection relevant to the active file.
func selectionNote(rpc *client.RPC, ac client.ActiveContext, sel client.ActiveSelection) string {
	if !sel.Found || !ac.Found || sel.BufID != ac.BufID {
		return ""
	}
	content := bufferContent(rpc, ac.FilePath)
	if content == "" {
		return ""
	}
	text := extractSelection(content, sel)
	if text == "" {
		return ""
	}
	return fmt.Sprintf("Selected text (lines %d-%d):\n```\n%s\n```\n\n",
		sel.StartLine+1, sel.EndLine+1, text)
}

// bufferSnippet returns ±radius lines around line (0-indexed) of filePath as a
// fenced block with the cursor line marked. Returns "" on error.
func bufferSnippet(rpc *client.RPC, filePath string, line, radius int) string {
	content := bufferContent(rpc, filePath)
	if content == "" {
		return ""
	}
	fileLines := strings.Split(content, "\n")
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
- Line numbers in snippets are 1-based. When the user names a line number (or
  says "at the cursor"), use insert_at_line — the inserted text becomes exactly
  that line. Use apply_edits only for replacing existing text.
- When the user refers to "the selection" or "the selected code", they mean the
  "Selected text" block in their message — operate on exactly that text.
- Approved edits are fully applied to the live buffer; never ask the user to
  approve or save an edit after the tool succeeds. The on-disk file stays stale
  until saved — call save_file on edited files before disk-based verification.
- Prefer small, focused edits over large rewrites.
- Explain your reasoning before using apply_edits.
- Each apply_edits call requires user approval; batch related edits into one call when possible.
- When the user asks a question that doesn't require editing, just answer.
- This is a text-only terminal interface. The user cannot share screenshots, images, or any visual media.
  If you need visual information, use your tools to read the relevant source code directly. Never ask for a screenshot.
`)
	return sb.String()
}

func buildUserMessage(text string, ac client.ActiveContext, snippet, selNote string) apiMessage {
	if !ac.Found {
		return userText(text)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "[Active file: %s, line %d]\n\n", ac.FilePath, ac.Line+1)
	sb.WriteString(selNote)
	if snippet != "" {
		sb.WriteString("Context around cursor:\n")
		sb.WriteString(snippet)
		sb.WriteString("\n\n")
	}
	sb.WriteString(text)
	return userText(sb.String())
}

// runAgent runs the direct-API agentic loop in a goroutine. It builds the
// cursor snippet and selection note itself (RPC calls) so the UI loop never
// blocks on them. model is an alias or full model ID; "" uses defaultModel.
func runAgent(prog *programLink, rpc *client.RPC, apiKey, model, workDir string, history []apiMessage, text string, ac client.ActiveContext, sel client.ActiveSelection) {
	ctx, cancel := context.WithCancel(context.Background())
	prog.setCancel(cancel)
	defer prog.setCancel(nil)

	var snippet string
	if ac.Found {
		snippet = bufferSnippet(rpc, ac.FilePath, int(ac.Line), 20)
	}
	history = append(history, buildUserMessage(text, ac, snippet, selectionNote(rpc, ac, sel)))

	system := buildSystemPrompt(workDir, ac)
	tools := allTools()

	for {
		var (
			textBuf    strings.Builder
			toolCalls  []streamToolEvent
			stopReason string
		)

		err := streamAPI(ctx, apiKey, model, system, history, tools, func(ev any) {
			switch e := ev.(type) {
			case streamTextEvent:
				textBuf.WriteString(e.text)
				prog.emit(agentTextDeltaMsg(e))
			case streamToolEvent:
				toolCalls = append(toolCalls, e)
				prog.emit(agentToolStartMsg{name: e.name})
			case streamUsageEvent:
				prog.emit(agentUsageMsg{ctxTokens: e.ctxTokens})
			case streamStopEvent:
				stopReason = e.stopReason
			}
		})
		if err != nil {
			prog.emit(agentErrorMsg{
				err:      fmt.Errorf("API error: %w", err),
				friendly: classifyAgentError(err.Error()),
			})
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
// model is an alias ("opus", "sonnet", …) or full model ID as understood by
// the claude CLI's own --model flag; "" lets claude use its configured default.
func runClaudeSubprocess(prog *programLink, rpc *client.RPC, workDir, prompt, sessionID, model string, ac client.ActiveContext, sel client.ActiveSelection) {
	var snippet string
	if ac.Found {
		snippet = bufferSnippet(rpc, ac.FilePath, int(ac.Line), 20)
	}
	selNote := selectionNote(rpc, ac, sel)

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
	if model != "" {
		args = append(args, "--model", model)
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
			" use mcp__indigo__read_file to read files (returns live buffer content, including unsaved edits)," +
			" mcp__indigo__apply_edits to replace existing text," +
			" and mcp__indigo__insert_at_line to insert new lines at an exact 1-based line number" +
			" (the inserted text becomes that line; always prefer it when the user names a line or says 'at the cursor')." +
			" Each edit shows the user a diff popup; once approved it is fully applied to the live buffer —" +
			" never ask the user to approve or save an edit after the tool succeeds." +
			" The on-disk file stays stale until saved, so before running disk-based commands (go build, tests, grep)" +
			" call mcp__indigo__save_file on every file you edited; then verify and finish the task yourself." +
			" The built-in Read, Edit, Write, MultiEdit, and NotebookEdit tools are disabled in this session;" +
			" Glob, Grep, and Bash remain available."
	}
	if ac.Found {
		sysPrompt += fmt.Sprintf(
			" The user is currently editing %s at line %d, column %d."+
				" When suggesting edits to this file, prefer editing that file first."+
				" Line numbers in the provided snippet are 1-based and reflect the live buffer.",
			ac.FilePath, ac.Line+1, ac.Col+1,
		)
	}
	if selNote != "" {
		sysPrompt += fmt.Sprintf(
			" The user has lines %d-%d selected in the editor;"+
				" 'the selection' or 'the selected code' means exactly the Selected text block in their message.",
			sel.StartLine+1, sel.EndLine+1,
		)
	}
	args = append(args, "--append-system-prompt", sysPrompt)

	ctx, cancel := context.WithCancel(context.Background())
	prog.setCancel(cancel)
	defer prog.setCancel(nil)

	// Prepend file context to the user prompt so Claude sees the relevant code
	// without needing a read_file tool call.
	userPrompt := prompt
	if ac.Found && (snippet != "" || selNote != "") {
		userPrompt = fmt.Sprintf("[File: %s, line %d]\n%sContext around cursor:\n%s\n\n%s",
			ac.FilePath, ac.Line+1, selNote, snippet, prompt)
	}

	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Dir = workDir
	// apply_edits blocks on the in-editor approval popup, so give MCP tool
	// calls a generous timeout (values in milliseconds). INDIGO_CLAUDE_HOOK
	// marks this subprocess as ours so the workspace hook script only gates
	// commands from this session, not other Claude Code sessions.
	cmd.Env = append(os.Environ(),
		"MCP_TIMEOUT=30000",
		"MCP_TOOL_TIMEOUT=600000",
		"INDIGO_CLAUDE_HOOK=1",
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
					Usage struct {
						InputTokens              int `json:"input_tokens"`
						CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
						CacheReadInputTokens     int `json:"cache_read_input_tokens"`
						OutputTokens             int `json:"output_tokens"`
					} `json:"usage"`
				} `json:"message"`
			}
			if json.Unmarshal([]byte(line), &wrapper) != nil {
				continue
			}
			u := wrapper.Message.Usage
			if ctx := u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens + u.OutputTokens; ctx > 0 {
				prog.emit(agentUsageMsg{ctxTokens: ctx})
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
				IsError      bool    `json:"is_error"`
				Result       string  `json:"result"`
				TotalCostUSD float64 `json:"total_cost_usd"`
				Usage        struct {
					InputTokens              int `json:"input_tokens"`
					CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
					CacheReadInputTokens     int `json:"cache_read_input_tokens"`
					OutputTokens             int `json:"output_tokens"`
				} `json:"usage"`
			}
			if json.Unmarshal([]byte(line), &result) != nil {
				continue
			}
			if result.IsError {
				prog.emit(agentErrorMsg{
					err:      fmt.Errorf("%s", result.Result),
					friendly: classifyAgentError(result.Result),
					prompt:   prompt,
				})
				cmd.Wait() //nolint:errcheck
				return
			}
			u := result.Usage
			ctx := u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens + u.OutputTokens
			if ctx > 0 || result.TotalCostUSD > 0 {
				prog.emit(agentUsageMsg{ctxTokens: ctx, costUSD: result.TotalCostUSD})
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
		prog.emit(agentErrorMsg{
			err:      fmt.Errorf("%s", msg),
			friendly: classifyAgentError(msg),
			prompt:   prompt,
		})
		return
	}
	prog.emit(agentDoneMsg{sessionID: newSessionID})
}
