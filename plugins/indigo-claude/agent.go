package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"sync"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/indiejames/indigo/internal/client"
)

// ─── program link ────────────────────────────────────────────────────────────

// programLink lets goroutines push tea.Msg values to the running program.
// The send field is set after tea.NewProgram returns its *Program.
type programLink struct {
	mu   sync.Mutex
	send func(tea.Msg)
}

func (pl *programLink) emit(msg tea.Msg) {
	pl.mu.Lock()
	fn := pl.send
	pl.mu.Unlock()
	if fn != nil {
		fn(msg)
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

// permissionRequestMsg asks the TUI to show a diff and get user approval.
// The agent goroutine blocks on replyCh until the user responds.
type permissionRequestMsg struct {
	file    string
	reason  string
	edits   []editSpec
	replyCh chan bool
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
`)
	return sb.String()
}

func buildUserMessage(text string, ac client.ActiveContext) apiMessage {
	if !ac.Found {
		return userText(text)
	}
	ctx := fmt.Sprintf("[Active file: %s, line %d]\n\n", ac.FilePath, ac.Line+1)
	return userText(ctx + text)
}

// runAgent runs the direct-API agentic loop in a goroutine.
func runAgent(prog *programLink, rpc *client.RPC, apiKey, workDir string, history []apiMessage, ac client.ActiveContext) {
	ctx := context.Background()
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

// ─── subprocess runner ───────────────────────────────────────────────────────

// runClaudeSubprocess runs `claude -p` as a subprocess and emits agent events.
// sessionID is empty for the first turn; subsequent turns pass --resume so Claude
// Code continues the conversation with full context.
func runClaudeSubprocess(prog *programLink, workDir, prompt, sessionID string, ac client.ActiveContext) {
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
	if ac.Found {
		sysPrompt := fmt.Sprintf(
			"The user is currently editing %s at line %d, column %d. "+
				"When suggesting edits to this file, prefer editing that file first.",
			ac.FilePath, ac.Line+1, ac.Col+1,
		)
		args = append(args, "--append-system-prompt", sysPrompt)
	}

	cmd := exec.CommandContext(context.Background(), "claude", args...)
	cmd.Dir = workDir
	cmd.Stdin = strings.NewReader(prompt)

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
		// lastTextLen tracks how much text we've already emitted for the current
		// assistant turn, so we only emit deltas as partial messages arrive.
		lastTextLen  int
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
			// Capture the session ID so we can resume it next turn.
			json.Unmarshal(ev["session_id"], &newSessionID) //nolint:errcheck

		case "assistant":
			// Parse content blocks; emit text deltas and tool-start notifications.
			var wrapper struct {
				Message struct {
					Content []struct {
						Type string `json:"type"`
						Text string `json:"text"`
						Name string `json:"name"`
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
					prog.emit(agentToolStartMsg{name: blk.Name})
				}
			}
			// Emit only new text since the last partial.
			text := fullText.String()
			if len(text) > lastTextLen {
				prog.emit(agentTextDeltaMsg{text: text[lastTextLen:]})
				lastTextLen = len(text)
			}

		case "tool_result":
			// A tool finished; reset delta counter for the next assistant segment.
			prog.emit(agentToolDoneMsg{name: "tool"})
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
