// Package agenttools implements indigo's agent-facing tool layer: the tool
// definitions, their implementations against a live indigo server, and the
// MCP server that exposes them to an agent over stdio.
//
// It lives here rather than inside the indigo-claude plugin because the two
// are genuinely separate things that only happened to ship together. The
// plugin is a chat TUI; this is the editor integration — the reason an agent
// can read a buffer with unsaved edits, ask the language server what a symbol
// binds to, or make an undoable edit instead of a blind disk write. Requiring
// the chat UI in order to get any of that was an accident of packaging, and
// leaving the code in the plugin meant the binary you register for editor
// integration was named after a chat client you might never run.
//
// Both front ends now use this package: the plugin (which supplies its own
// TUI-backed approval prompt) and `indigo --mcp` (where approval is the MCP
// client's job).
package agenttools

// ToolDef describes one tool to an agent.
type ToolDef struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	InputSchema ToolSchema `json:"input_schema"`
}

// ToolSchema is a tool's JSON-Schema input description.
type ToolSchema struct {
	Type       string                `json:"type"`
	Properties map[string]SchemaProp `json:"properties"`
	Required   []string              `json:"required,omitempty"`
}

// SchemaProp is one property in a ToolSchema.
type SchemaProp struct {
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
}

// EditSpec is one old→new replacement, shown to whoever approves the edit.
type EditSpec struct {
	Path    string
	OldText string
	NewText string
}

// EditRequest is a proposed change to the user's code, passed to an Approver
// before anything is applied.
type EditRequest struct {
	File   string
	Reason string
	Edits  []EditSpec
}

// Approver decides whether a proposed edit may proceed.
//
// This is the one thing the two front ends genuinely differ on, so it is the
// only thing they have to supply. The plugin shows a popup in its TUI and
// waits for the user; `indigo --mcp` has no UI and returns true, because the
// MCP client has already prompted before calling the tool at all.
//
// ApproveEdit must not block indefinitely: an implementation with nowhere to
// ask has to answer, not wait. (An earlier version of the standalone path
// reused the TUI's approver with no program attached, which blocked forever
// on a reply channel that nothing could ever write to.)
type Approver interface {
	ApproveEdit(EditRequest) bool
}

// AlwaysApprove is an Approver for front ends where approval happened before
// the call reached us.
type AlwaysApprove struct{}

func (AlwaysApprove) ApproveEdit(EditRequest) bool { return true }
