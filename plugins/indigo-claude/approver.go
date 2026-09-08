package main

import "github.com/indiejames/indigo/internal/agenttools"

// tuiApprover is the chat TUI's implementation of agenttools.Approver: it
// shows the permission popup and blocks the tool-exec goroutine until the user
// answers, unless auto-approve for edits is on (/autoapprove edits on).
//
// This is the one behaviour the plugin has that `indigo --mcp` does not, and
// it is the reason agenttools takes an interface rather than owning approval
// itself.
type tuiApprover struct{ prog *programLink }

func (a tuiApprover) ApproveEdit(req agenttools.EditRequest) bool {
	if edits, _ := a.prog.autoApprove(); edits {
		return true
	}
	replyCh := make(chan bool, 1)
	a.prog.emit(permissionRequestMsg{
		file:    req.File,
		reason:  req.Reason,
		edits:   toEditSpecs(req.Edits),
		replyCh: replyCh,
	})
	return <-replyCh
}

// editSpec is one old→new replacement shown in the permission prompt. It
// mirrors agenttools.EditSpec rather than reusing it because the popup
// renderer is built around the plugin's own unexported message types.
type editSpec struct {
	path    string
	oldText string
	newText string
}

func toEditSpecs(in []agenttools.EditSpec) []editSpec {
	out := make([]editSpec, 0, len(in))
	for _, e := range in {
		out = append(out, editSpec{path: e.Path, oldText: e.OldText, newText: e.NewText})
	}
	return out
}
