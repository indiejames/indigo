// Package debug is indigo's server-side debugger support: the breakpoint store
// and the debug session, on top of internal/dap. It depends on nothing that
// needs cgo, so it builds into the static server copied into dev containers.
//
// Lines are 0-based throughout, as everywhere else in indigo; the conversion
// to DAP's 1-based lines happens only where this package talks to the adapter.
package debug

import (
	"sort"
	"strings"
	"sync"

	"github.com/indiejames/indigo/internal/dap"
	"github.com/indiejames/indigo/internal/document"
)

// Breakpoint is one line breakpoint.
type Breakpoint struct {
	Path string
	Line int // 0-based
	// Verified and Message are the adapter's answer when a session is
	// running: whether the breakpoint could be set, and why not. A breakpoint
	// set with no session running is unverified until one starts.
	Verified bool
	Message  string
	// Condition makes the program stop only when it evaluates true, in the
	// debugged language. LogMessage makes this a logpoint: the adapter
	// prints it — with {expressions} interpolated — and does not stop.
	Condition  string
	LogMessage string

	// answers is what each of a session's connections said about this
	// breakpoint, in the order they first answered — the root first, since
	// it is the connection the session opens with. Verified and Message
	// above are derived from it by recompute.
	//
	// Per connection rather than one shared answer because a session is a
	// tree and each child is a separate program (see children.go): the root
	// of a js-debug session does not debug the program at all and answers
	// "not verified" for every breakpoint, while the child that does answers
	// "verified". With one shared field, whichever reply landed last won —
	// and at session start the two are sent from different goroutines, so a
	// live breakpoint was drawn as one that could not be set, or not,
	// depending on a race.
	answers []connAnswer
}

// connAnswer is one connection's answer about one breakpoint.
type connAnswer struct {
	conn     *dap.Client
	id       int // the adapter's id for it, for matching its later events; 0 when it gave none
	verified bool
	message  string
}

// record stores conn's answer, replacing its previous one.
func (bp *Breakpoint) record(conn *dap.Client, a connAnswer) {
	a.conn = conn
	for i := range bp.answers {
		if bp.answers[i].conn == conn {
			bp.answers[i] = a
			return
		}
	}
	bp.answers = append(bp.answers, a)
}

// answerFrom returns conn's answer, and whether it gave one.
func (bp *Breakpoint) answerFrom(conn *dap.Client) (connAnswer, bool) {
	for _, a := range bp.answers {
		if a.conn == conn {
			return a, true
		}
	}
	return connAnswer{}, false
}

// forget drops conn's answer, for a connection that has gone away.
func (bp *Breakpoint) forget(conn *dap.Client) {
	for i := range bp.answers {
		if bp.answers[i].conn == conn {
			bp.answers = append(bp.answers[:i], bp.answers[i+1:]...)
			return
		}
	}
}

// recompute derives Verified and Message from the per-connection answers.
//
// Verified is a disjunction: a breakpoint set in *any* of a session's programs
// is set, whatever the connections debugging the others say about it. Message
// only means anything when nothing verified it, and is then the first
// connection with something to say — the root, where a "this debugger does not
// support logpoints" originates, before any child.
func (bp *Breakpoint) recompute() {
	bp.Verified, bp.Message = false, ""
	for _, a := range bp.answers {
		if a.verified {
			bp.Verified = true
			return
		}
	}
	for _, a := range bp.answers {
		if a.message != "" {
			bp.Message = a.message
			return
		}
	}
}

// Breakpoints is the server's breakpoint store: one set, shared by every
// window, kept on the right line as the file is edited.
type Breakpoints struct {
	mu     sync.Mutex
	byPath map[string][]*Breakpoint
	seq    uint64 // bumped on every change; windows refetch when it moves
}

// NewBreakpoints returns an empty store.
func NewBreakpoints() *Breakpoints {
	return &Breakpoints{byPath: map[string][]*Breakpoint{}}
}

// Toggle adds a breakpoint at line, or removes the one already there. It
// reports whether a breakpoint is now set.
func (b *Breakpoints) Toggle(path string, line int) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	bps := b.byPath[path]
	for i, bp := range bps {
		if bp.Line == line {
			b.byPath[path] = append(bps[:i], bps[i+1:]...)
			if len(b.byPath[path]) == 0 {
				delete(b.byPath, path)
			}
			b.seq++
			return false
		}
	}
	b.byPath[path] = append(bps, &Breakpoint{Path: path, Line: line})
	sortByLine(b.byPath[path])
	b.seq++
	return true
}

// Set puts a breakpoint at line with the given condition and log message,
// replacing either on one already there. It reports whether anything changed.
func (b *Breakpoints) Set(path string, line int, condition, logMessage string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, bp := range b.byPath[path] {
		if bp.Line == line {
			if bp.Condition == condition && bp.LogMessage == logMessage {
				return false
			}
			bp.Condition, bp.LogMessage = condition, logMessage
			// Changed: no connection has confirmed this form of it. The old
			// answers must go too, or recompute would restore a "verified"
			// given for the previous condition.
			bp.Verified, bp.Message, bp.answers = false, "", nil
			b.seq++
			return true
		}
	}
	b.byPath[path] = append(b.byPath[path], &Breakpoint{Path: path, Line: line, Condition: condition, LogMessage: logMessage})
	sortByLine(b.byPath[path])
	b.seq++
	return true
}

// List returns the breakpoints in path, or in every file when path is "",
// ordered by path then line, and the store's current sequence number.
func (b *Breakpoints) List(path string) ([]Breakpoint, uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var paths []string
	if path != "" {
		paths = []string{path}
	} else {
		for p := range b.byPath {
			paths = append(paths, p)
		}
		sort.Strings(paths)
	}
	var out []Breakpoint
	for _, p := range paths {
		for _, bp := range b.byPath[p] {
			c := *bp
			// The copy must not alias the store's per-connection answers:
			// the slice header would be shared, and record/forget mutate
			// that backing array under the lock a caller no longer holds.
			// Nothing outside this file reads them — Verified and Message
			// are what they are for.
			c.answers = nil
			out = append(out, c)
		}
	}
	return out, b.seq
}

// Paths returns every file that has a breakpoint.
func (b *Breakpoints) Paths() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, 0, len(b.byPath))
	for p := range b.byPath {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// Seq is the store's current sequence number.
func (b *Breakpoints) Seq() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.seq
}

// ApplyEdit moves path's breakpoints to follow op, and removes any on a line
// the edit deleted. It reports whether anything changed.
//
// The rule is "a breakpoint stays with its line's text":
//
//   - An insert at column 0 of line L puts the new lines *before* L, so a
//     breakpoint on L moves down with it. An insert after column 0 splits L,
//     and the breakpoint stays on L's first part.
//   - A delete removes the lines it covers. A breakpoint on a removed line is
//     removed; one below moves up. A delete that runs from column 0 to column
//     0 removes whole lines [from, to); one that joins lines removes
//     (from, to] — the lines merged into the first.
//
// The per-edit line deltas the server hands plugins cannot express this: they
// say only "n lines at line L", which is the same for inserting before a line
// and splitting it after its first character.
func (b *Breakpoints) ApplyEdit(path string, op document.Op) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	bps := b.byPath[path]
	if len(bps) == 0 {
		return false
	}
	changed := false
	kept := bps[:0]
	for _, bp := range bps {
		line, ok := shiftLine(bp.Line, op)
		if !ok {
			changed = true
			continue
		}
		if line != bp.Line {
			bp.Line = line
			// Moved: no connection has confirmed the new line, and the old
			// answers were about the old one.
			bp.Verified, bp.Message, bp.answers = false, "", nil
			changed = true
		}
		kept = append(kept, bp)
	}
	// An edit can land two breakpoints on one line (a join); keep one.
	kept = dedupeLines(kept)
	if len(kept) == 0 {
		delete(b.byPath, path)
	} else {
		b.byPath[path] = kept
	}
	if changed {
		b.seq++
	}
	return changed
}

// shiftLine is where a breakpoint on line ends up after op, and false if op
// deleted that line.
func shiftLine(line int, op document.Op) (int, bool) {
	switch op.Type {
	case document.OpInsert:
		n := strings.Count(op.InsertText, "\n")
		if n == 0 {
			return line, true
		}
		if line > op.InsertLine || (line == op.InsertLine && op.InsertCol == 0) {
			return line + n, true
		}
		return line, true
	case document.OpDelete:
		if op.FromLine == op.ToLine {
			return line, true
		}
		removed := op.ToLine - op.FromLine
		if op.FromCol == 0 && op.ToCol == 0 {
			// Whole lines [FromLine, ToLine) are gone; ToLine moves up.
			switch {
			case line < op.FromLine:
				return line, true
			case line < op.ToLine:
				return 0, false
			default:
				return line - removed, true
			}
		}
		// Lines FromLine+1..ToLine are joined onto FromLine.
		switch {
		case line <= op.FromLine:
			return line, true
		case line <= op.ToLine:
			return 0, false
		default:
			return line - removed, true
		}
	}
	return line, true
}

// setResults records the adapter's answer for path's breakpoints, in the order
// they were sent. An adapter may move a breakpoint (one on a blank line goes to
// the next statement); the marker follows it, as it does in VS Code.
func (b *Breakpoints) setResults(path string, conn *dap.Client, sent []int, results []resultLine) {
	b.mu.Lock()
	defer b.mu.Unlock()
	bps := b.byPath[path]
	for i, line := range sent {
		if i >= len(results) {
			break
		}
		for _, bp := range bps {
			if bp.Line != line {
				continue
			}
			a := connAnswer{id: results[i].id, verified: results[i].verified, message: results[i].message}
			if conn == nil {
				// No connection named: the answer still counts, but an id
				// under a nil key could not be matched back to a sender.
				a.id = 0
			}
			bp.record(conn, a)
			bp.recompute()
			if results[i].verified && results[i].line >= 0 {
				bp.Line = results[i].line
			}
		}
	}
	b.byPath[path] = dedupeLines(bps)
	sortByLine(b.byPath[path])
	b.seq++
}

// updateByID applies an adapter's later change to the breakpoint conn knows as
// id, and reports whether there was one.
func (b *Breakpoints) updateByID(conn *dap.Client, r resultLine) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for path, bps := range b.byPath {
		for _, bp := range bps {
			prev, ok := bp.answerFrom(conn)
			if !ok || prev.id == 0 || prev.id != r.id {
				continue
			}
			bp.record(conn, connAnswer{id: r.id, verified: r.verified, message: r.message})
			bp.recompute()
			if r.verified && r.line >= 0 {
				bp.Line = r.line
			}
			b.byPath[path] = dedupeLines(bps)
			sortByLine(b.byPath[path])
			b.seq++
			return true
		}
	}
	return false
}

// resetVerification marks every breakpoint unverified, for when a session ends.
func (b *Breakpoints) resetVerification() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, bps := range b.byPath {
		for _, bp := range bps {
			bp.Verified, bp.Message = false, ""
			bp.answers = nil
		}
	}
	b.seq++
}

// ForgetConnection drops everything a connection said, for a child session
// that has ended. Its answers must not outlive it: a breakpoint is verified
// while *some* connection has it set, so a departed child's "verified" would
// otherwise keep a breakpoint looking live in a program that is gone.
func (b *Breakpoints) ForgetConnection(conn *dap.Client) {
	b.mu.Lock()
	defer b.mu.Unlock()
	changed := false
	for _, bps := range b.byPath {
		for _, bp := range bps {
			before := bp.Verified
			bp.forget(conn)
			bp.recompute()
			if bp.Verified != before {
				changed = true
			}
		}
	}
	if changed {
		b.seq++
	}
}

type resultLine struct {
	id       int // the adapter's id for it; 0 when it gave none
	verified bool
	line     int // 0-based; -1 when the adapter did not say
	message  string
}

func sortByLine(bps []*Breakpoint) {
	sort.Slice(bps, func(i, j int) bool { return bps[i].Line < bps[j].Line })
}

func dedupeLines(bps []*Breakpoint) []*Breakpoint {
	sortByLine(bps)
	out := bps[:0]
	for i, bp := range bps {
		if i > 0 && bp.Line == bps[i-1].Line {
			continue
		}
		out = append(out, bp)
	}
	return out
}
