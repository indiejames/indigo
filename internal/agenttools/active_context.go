package agenttools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/indiejames/indigo/internal/rpcclient"
)

// get_active_context answers "which file is open, and where is the cursor?" —
// what an agent needs for "the current file", "line 30 of this file", "at the
// cursor", or "the selected code".
//
// Every indigo window already reports its active buffer, cursor and selection
// to the server as they change (SetActiveContext/SetActiveSelection). The
// in-editor chat plugin read that and handed it to the model; when it was
// removed, the MCP tools that replaced it never exposed it, so an agent asked
// about "the currently open file" had no way to find out which one that was.

// execGetActiveContext reads the most recent active context and selection from
// the server and reports them.
func execGetActiveContext(ctx context.Context, rpc *rpcclient.RPC) (string, bool) {
	ac, err := rpc.GetActiveContext(ctx)
	if err != nil {
		return fmt.Sprintf("could not read the editor's active context: %v", err), true
	}
	sel, _ := rpc.GetActiveSelection(ctx) // zero value on error = no selection
	openCount, countErr := uint32(1), error(nil)
	if ac.Found {
		openCount, countErr = rpc.BufferClientCount(ctx, ac.BufID)
	}
	return formatActiveContext(ac, sel, openCount, countErr, time.Now()), false
}

// formatActiveContext renders the result. Split from execGetActiveContext so
// every case can be tested without a server.
//
// Lines and columns are reported 1-based, the numbering insert_at_line,
// goto_file and read_file all take, so the answer can be passed straight to
// them; the server stores them 0-based.
//
// "Active" means the window that most recently reported, across every window
// on this workspace. That is stated in the output along with its age, because
// with two windows open it is the one last *touched*, which is usually but not
// necessarily the one the user means.
func formatActiveContext(ac rpcclient.ActiveContext, sel rpcclient.ActiveSelection, openCount uint32, countErr error, now time.Time) string {
	if !ac.Found {
		return "No editor window has reported an active file. Is an indigo window open on this workspace? " +
			"(The active file is reported by the editor window, not by this tool's connection.)"
	}
	var b strings.Builder
	if ac.FilePath == "" {
		b.WriteString("Active buffer: untitled (not saved to a file, so file-based tools cannot address it)\n")
	} else {
		fmt.Fprintf(&b, "Active file: %s\n", ac.FilePath)
	}
	fmt.Fprintf(&b, "Cursor: line %d, column %d (1-based)\n", ac.Line+1, ac.Col+1)

	if sel.Found && sel.BufID == ac.BufID {
		if sel.IsLine {
			fmt.Fprintf(&b, "Selection: lines %d-%d (whole lines)\n", sel.StartLine+1, sel.EndLine+1)
		} else {
			fmt.Fprintf(&b, "Selection: line %d column %d to line %d column %d (inclusive)\n",
				sel.StartLine+1, sel.StartCol+1, sel.EndLine+1, sel.EndCol+1)
		}
	} else {
		b.WriteString("Selection: none\n")
	}

	if !ac.UpdatedAt.IsZero() {
		fmt.Fprintf(&b, "Reported %s ago by the most recently active window.", now.Sub(ac.UpdatedAt).Round(time.Second))
	}
	switch {
	case countErr != nil:
		fmt.Fprintf(&b, "\n(Could not confirm the buffer is still open: %v)", countErr)
	case openCount == 0:
		b.WriteString("\nWARNING: that buffer has since been closed in every window, so this is where the user " +
			"*was*, not what is open now.")
	}
	return b.String()
}
