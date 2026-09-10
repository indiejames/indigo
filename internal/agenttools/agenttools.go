// Package agenttools implements indigo's agent-facing tool layer: the tool
// definitions, their implementations against a live indigo server, and the
// MCP server that exposes them to an agent over stdio.
//
// This is the editor integration — the reason an agent can read a buffer with
// unsaved edits, ask the language server what a symbol binds to, or make an
// undoable edit instead of a blind disk write.
//
// It was extracted here from the indigo-claude chat plugin, which used to be
// the only way to get any of it: requiring a chat UI for editor integration
// was an accident of packaging, and it meant the binary you registered with
// your agent was named after a chat client you might never run. `indigo --mcp`
// and `indigo --mcp-http` are now the only front ends, and that plugin has
// since been removed altogether.
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
// Approval is the one thing a front end has to supply, and today every front
// end here answers the same way — AlwaysApprove, because the MCP client has
// already prompted before calling the tool at all. The seam is kept because
// approval is a policy decision the tool layer should not be making on its
// own: it was a chat TUI's own popup before, and a front end that can ask a
// human should be able to say so again without threading a flag through every
// edit tool.
//
// ApproveEdit must not block indefinitely: an implementation with nowhere to
// ask has to answer, not wait. (An earlier version of the standalone path
// reused a UI-backed approver with no program attached, which blocked forever
// on a reply channel that nothing could ever write to.)
type Approver interface {
	ApproveEdit(EditRequest) bool
}

// AlwaysApprove is an Approver for front ends where approval happened before
// the call reached us.
type AlwaysApprove struct{}

func (AlwaysApprove) ApproveEdit(EditRequest) bool { return true }
