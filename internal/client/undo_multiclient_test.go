package client

import (
	"math/rand"

	"github.com/indiejames/indigo/internal/document"
	"os"
	"path/filepath"
	"testing"
)

// editUndoable applies an op through applyOp, the path a keystroke takes, which
// records the op for undo. The convergence fuzz's edit() deliberately uses the
// leaner sendOp path, which does not.
func (c *otClient) editUndoable(t *testing.T, op document.Op) {
	t.Helper()
	m, _ := applyOp(c.m, op)
	c.m = m
}

// undo runs one undo and flushes the resulting ops to the server. executeUndo
// returns its send commands inside a batch this harness does not run, so the
// queue is drained directly.
func (c *otClient) undo(t *testing.T) {
	t.Helper()
	updated, _ := executeUndo(c.m)
	c.m = updated.(Model)
	c.drainNow(t)
}

// drainNow flushes whatever is queued, regardless of which command was returned
// where — drainCmd pops from the shared queue.
func (c *otClient) drainNow(t *testing.T) {
	t.Helper()
	cmd := c.m.drainCmd(c.m.sendQ.currentEpoch())
	c.runCmd(t, func() any { return cmd() })
}

// TestUndoAfterRemoteEditRemovesTheRightText reproduces the two-window report:
// type in one window, receive an edit from the other, then undo. The undo must
// remove the text it recorded, not text somewhere else.
func TestUndoAfterRemoteEditRemovesTheRightText(t *testing.T) {
	dir, sock := startOTServer(t)
	path := filepath.Join(dir, "u.txt")
	if err := os.WriteFile(path, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := dialOTClient(t, sock, path, dir, "A")
	b := dialOTClient(t, sock, path, dir, "B")

	// A appends "X" after "hello".
	a.editUndoable(t, opIns(0, 5, "X"))
	a.drainNow(t)
	b.settle(t)
	if got := b.m.buf.Content(); got != "helloX\n" {
		t.Fatalf("B after A's edit: %q, want %q", got, "helloX\n")
	}

	// B inserts "Y" at the very start.
	b.editUndoable(t, opIns(0, 0, "Y"))
	b.drainNow(t)
	a.settle(t)
	if got := a.m.buf.Content(); got != "YhelloX\n" {
		t.Fatalf("A after B's edit: %q, want %q", got, "YhelloX\n")
	}

	// A undoes twice. Remote ops are made undoable and pushed on top, so the
	// first undo reverts B's "Y" and the second reverts A's own "X".
	a.undo(t)
	t.Logf("A after first undo:  %q", a.m.buf.Content())
	a.undo(t)
	t.Logf("A after second undo: %q", a.m.buf.Content())

	if got := a.m.buf.Content(); got != "hello\n" {
		t.Errorf("A after undoing both edits: %q, want %q", got, "hello\n")
	}

	// And both windows must still agree.
	a.settle(t)
	b.settle(t)
	if a.m.buf.Content() != b.m.buf.Content() {
		t.Errorf("windows diverged after undo:\n  A: %q\n  B: %q", a.m.buf.Content(), b.m.buf.Content())
	}
}

// startInsertSession mimics entering insert mode: subsequent applyOp calls
// append their inverses to currentGroup instead of pushing an entry each.
func (c *otClient) startInsertSession() {
	c.m.currentGroup = []document.Op{}
}

// endInsertSession mimics Esc, pushing the session as one undo entry.
func (c *otClient) endInsertSession() {
	if len(c.m.currentGroup) > 0 {
		c.m.undoStack = append(c.m.undoStack, undoEntry{ops: c.m.currentGroup, before: c.m.cursorSnap()})
	}
	c.m.currentGroup = nil
}

// TestUndoOfInsertSessionInterruptedByRemoteEdit reproduces the reported
// two-window failure more closely: a remote edit lands *during* an insert
// session, so the session's inverses are rebased while it is still open, and the
// whole session is then undone as one entry.
func TestUndoOfInsertSessionInterruptedByRemoteEdit(t *testing.T) {
	dir, sock := startOTServer(t)
	path := filepath.Join(dir, "u2.txt")
	if err := os.WriteFile(path, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := dialOTClient(t, sock, path, dir, "A")
	b := dialOTClient(t, sock, path, dir, "B")

	a.startInsertSession()
	a.editUndoable(t, opIns(0, 5, "1")) // "hello1"
	a.drainNow(t)

	// B edits at the very start while A's session is still open.
	b.settle(t)
	b.editUndoable(t, opIns(0, 0, "Y"))
	b.drainNow(t)
	a.settle(t) // A integrates it: "Yhello1"
	if got := a.m.buf.Content(); got != "Yhello1\n" {
		t.Fatalf("A mid-session: %q, want %q", got, "Yhello1\n")
	}

	// A keeps typing where its cursor now is.
	a.editUndoable(t, opIns(0, 7, "2"))
	a.editUndoable(t, opIns(0, 8, "3"))
	a.drainNow(t)
	if got := a.m.buf.Content(); got != "Yhello123\n" {
		t.Fatalf("A after typing: %q, want %q", got, "Yhello123\n")
	}

	a.endInsertSession()
	// The session was split by B's edit, so undoing all of A's own typing takes
	// more than one step — and B's entry sits between the halves.
	for len(a.m.undoStack) > 0 {
		a.undo(t)
	}
	t.Logf("A after undoing everything: %q", a.m.buf.Content())

	if got := a.m.buf.Content(); got != "hello\n" {
		t.Errorf("A after undoing everything: %q, want %q", got, "hello\n")
	}

	a.settle(t)
	b.settle(t)
	if a.m.buf.Content() != b.m.buf.Content() {
		t.Errorf("windows diverged after undo:\n  A: %q\n  B: %q", a.m.buf.Content(), b.m.buf.Content())
	}
}

// TestUndoEverythingReturnsToOriginalFuzz is the sharp oracle for undo under
// concurrency. Remote ops are themselves made undoable — their inverses are
// pushed as entries — so a client's undo stack accounts for *every* op its
// buffer has seen. Undoing all of them must therefore return that buffer to
// exactly the content it started with.
//
// Convergence alone cannot catch this class: if an undo applies at the wrong
// coordinates, the resulting op is sent and both windows converge on the same
// wrong content. Only an absolute invariant sees it.
func TestUndoEverythingReturnsToOriginalFuzz(t *testing.T) {
	const original = "alpha\nbeta\ngamma\n"
	texts := []string{"x", "yz", "\n", "q\nr"}

	for seed := 0; seed < 60; seed++ {
		func() {
			dir, sock := startOTServer(t)
			path := filepath.Join(dir, "f.txt")
			if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
				t.Fatal(err)
			}
			a := dialOTClient(t, sock, path, dir, "A")
			b := dialOTClient(t, sock, path, dir, "B")
			clients := []*otClient{a, b}

			rnd := newSeededRand(int64(seed))
			for r := 0; r < 14; r++ {
				c := clients[rnd.Intn(2)]
				switch rnd.Intn(3) {
				case 0, 1:
					line := rnd.Intn(c.m.buf.LineCount())
					col := rnd.Intn(c.m.buf.LineLen(line) + 1)
					c.editUndoable(t, opIns(line, col, texts[rnd.Intn(len(texts))]))
					c.drainNow(t)
				default:
					c.settle(t)
				}
			}

			// Quiesce, then undo everything on A.
			for i := 0; i < 4; i++ {
				a.settle(t)
				b.settle(t)
			}
			// Undo with B still active for the first half: remote ops arriving
			// *between* undos is the case a quiesced test never reaches, and
			// the one the two-window report described.
			bStillEditing := true
			for len(a.m.undoStack) > 0 {
				if bStillEditing && rnd.Intn(3) == 0 {
					line := rnd.Intn(b.m.buf.LineCount())
					col := rnd.Intn(b.m.buf.LineLen(line) + 1)
					b.editUndoable(t, opIns(line, col, texts[rnd.Intn(len(texts))]))
					b.drainNow(t)
					a.settle(t)
				}
				if len(a.m.undoStack) <= 2 {
					// Let it finish cleanly so the invariant is well defined.
					bStillEditing = false
					for i := 0; i < 3; i++ {
						a.settle(t)
						b.settle(t)
					}
				}
				before := len(a.m.undoStack)
				a.undo(t)
				if len(a.m.undoStack) >= before {
					t.Fatalf("seed %d: undo did not shrink the stack (%d entries)", seed, before)
				}
			}
			for i := 0; i < 3; i++ {
				a.settle(t)
				b.settle(t)
			}
			for len(a.m.undoStack) > 0 {
				a.undo(t)
			}

			if got := a.m.buf.Content(); got != original {
				t.Fatalf("seed %d: undoing everything gave %q, want the original %q",
					seed, got, original)
			}
		}()
	}
}

func newSeededRand(seed int64) *rand.Rand { return rand.New(rand.NewSource(seed)) }

// containsInOrder reports whether every rune of want appears in s in order.
func containsInOrder(s, want string) bool {
	i := 0
	for _, r := range s {
		if i < len(want) && rune(want[i]) == r {
			i++
		}
	}
	return i == len(want)
}

// TestUndoNeverTouchesPreExistingText is the reported symptom stated as an
// invariant. Every edit here is an insertion, so the original characters can
// never legitimately be removed — not by an edit, and not by undoing one. If an
// undo applies at stale coordinates it eats text it never inserted, and this
// sees it the moment it happens rather than at the end.
//
// The shape mirrors the two-window report: insert *sessions* (many ops undone as
// one entry, the insert-mode path) alternating between windows, then repeated
// undo on one of them.
func TestUndoNeverTouchesPreExistingText(t *testing.T) {
	const original = "0123456789\n"
	letters := []string{"a", "b", "c", "d", "e"}

	for seed := 0; seed < 80; seed++ {
		func() {
			dir, sock := startOTServer(t)
			path := filepath.Join(dir, "p.txt")
			if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
				t.Fatal(err)
			}
			a := dialOTClient(t, sock, path, dir, "A")
			b := dialOTClient(t, sock, path, dir, "B")
			clients := []*otClient{a, b}
			rnd := newSeededRand(int64(seed) + 9000)

			// Alternate insert sessions between the two windows.
			for round := 0; round < 6; round++ {
				c := clients[round%2]
				c.settle(t)
				c.startInsertSession()
				for k := 0; k < 1+rnd.Intn(3); k++ {
					line := rnd.Intn(c.m.buf.LineCount())
					col := rnd.Intn(c.m.buf.LineLen(line) + 1)
					c.editUndoable(t, opIns(line, col, letters[rnd.Intn(len(letters))]))
				}
				c.endInsertSession()
				c.drainNow(t)
			}
			for i := 0; i < 4; i++ {
				a.settle(t)
				b.settle(t)
			}

			// Now undo repeatedly on A, checking the invariant after each step.
			step := 0
			for len(a.m.undoStack) > 0 {
				a.undo(t)
				step++
				got := a.m.buf.Content()
				if !containsInOrder(got, "0123456789") {
					t.Fatalf("seed %d: undo #%d removed pre-existing text\n  content: %q",
						seed, step, got)
				}
			}
			if got := a.m.buf.Content(); got != original {
				t.Fatalf("seed %d: after undoing everything: %q, want %q", seed, got, original)
			}
		}()
	}
}

// TestUndoWithMixedSessionOpsFuzz covers what real typing actually produces:
// insert sessions containing corrections, so one undo entry holds both inserts
// and deletes and is replayed in reverse. Insert-only fuzzing never builds that
// shape, and it is the shape the two-window report was made against.
func TestUndoWithMixedSessionOpsFuzz(t *testing.T) {
	const original = "0123456789\nabcdefghij\n"

	for seed := 0; seed < 80; seed++ {
		func() {
			dir, sock := startOTServer(t)
			path := filepath.Join(dir, "m.txt")
			if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
				t.Fatal(err)
			}
			a := dialOTClient(t, sock, path, dir, "A")
			b := dialOTClient(t, sock, path, dir, "B")
			clients := []*otClient{a, b}
			rnd := newSeededRand(int64(seed) + 4242)

			for round := 0; round < 6; round++ {
				c := clients[round%2]
				c.settle(t)
				c.startInsertSession()
				for k := 0; k < 1+rnd.Intn(3); k++ {
					line := rnd.Intn(c.m.buf.LineCount())
					lineLen := c.m.buf.LineLen(line)
					if rnd.Intn(3) == 0 && lineLen > 1 {
						// A correction: delete a rune, as backspace does.
						col := rnd.Intn(lineLen)
						c.editUndoable(t, document.Op{
							Type: document.OpDelete, FromLine: line, FromCol: col,
							ToLine: line, ToCol: col + 1,
						})
						continue
					}
					c.editUndoable(t, opIns(line, rnd.Intn(lineLen+1), "z"))
				}
				c.endInsertSession()
				c.drainNow(t)
			}
			for i := 0; i < 4; i++ {
				a.settle(t)
				b.settle(t)
			}

			step := 0
			for len(a.m.undoStack) > 0 {
				before := len(a.m.undoStack)
				a.undo(t)
				step++
				if len(a.m.undoStack) >= before {
					t.Fatalf("seed %d: undo #%d did not shrink the stack", seed, step)
				}
			}
			if got := a.m.buf.Content(); got != original {
				t.Fatalf("seed %d: after undoing everything: %q\n                        want %q",
					seed, got, original)
			}
		}()
	}
}

// TestUndoWithRemoteEditMidInsertSession reproduces the two-window report
// exactly, and the failure it describes.
//
// An insert session that spans a remote edit breaks the undo stack's ordering
// invariant. Typing before the remote op and typing after it both land in one
// currentGroup, which is pushed as a single entry at Esc — *above* the entry
// recorded for the remote op. The stack therefore claims the remote op happened
// before all of that typing, when half of it came first. Undoing the session
// removes ops the remote op's inverse depended on, so the next undo applies at
// coordinates that no longer describe anything, and starts eating pre-existing
// text.
func TestUndoWithRemoteEditMidInsertSession(t *testing.T) {
	const original = "AAA\nCOMMENT\n"
	dir, sock := startOTServer(t)
	path := filepath.Join(dir, "s.txt")
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	a := dialOTClient(t, sock, path, dir, "A") // "client 1" in the report
	b := dialOTClient(t, sock, path, dir, "B") // "client 2", the one that undoes

	// B opens an insert session and creates a braced region, as typing an
	// auto-paired "{" does.
	b.startInsertSession()
	b.editUndoable(t, opIns(0, 3, "{\n}"))
	b.drainNow(t)

	// A types *inside* that new region while B's session is still open.
	a.settle(t)
	a.editUndoable(t, opIns(1, 0, "FOO"))
	a.drainNow(t)
	b.settle(t)

	// B types more in the same session, then leaves insert mode.
	b.editUndoable(t, opIns(0, 4, "Z"))
	b.drainNow(t)
	b.endInsertSession()

	t.Logf("before undo: %q", b.m.buf.Content())

	for i := 1; len(b.m.undoStack) > 0; i++ {
		b.undo(t)
		got := b.m.buf.Content()
		t.Logf("after undo #%d: %q", i, got)
		if !containsInOrder(got, "COMMENT") {
			t.Fatalf("undo #%d ate pre-existing text:\n  %q", i, got)
		}
	}
	if got := b.m.buf.Content(); got != original {
		t.Errorf("after undoing everything: %q, want %q", got, original)
	}
}
