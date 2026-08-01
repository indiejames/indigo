package client

import (
	"reflect"
	"testing"

	"github.com/indiejames/indigo/internal/document"
)

func TestParseSignatureParams(t *testing.T) {
	cases := []struct {
		detail string
		want   []string
	}{
		// Imported function (alias) — the case that motivated this.
		{"(alias) function greetLoudly(name: string): string\nimport greetLoudly", []string{"name"}},
		// Plain function, multiple params.
		{"function greet(name: string, times: number): void", []string{"name", "times"}},
		// Method.
		{"(method) Foo.bar(x: number, y: number): void", []string{"x", "y"}},
		// No-arg call.
		{"function now(): number", nil},
		// Generic function — angle brackets, and a generic-with-comma param type.
		{"function pick<T>(obj: T, keys: Map<string, number>): T", []string{"obj", "keys"}},
		// Arrow-typed value (callback param inside protected by parens).
		{"function each(cb: (x: number, i: number) => void, thisArg: any): void", []string{"cb", "thisArg"}},
		// Optional and rest params.
		{"function f(a?: number, ...rest: string[]): void", []string{"a", "rest"}},
		// Arrow-typed variable.
		{"const cb: (x: number) => void", []string{"x"}},
		// Non-callable — no parens.
		{"const x: number", nil},
		// Destructured param falls back to a generated name.
		{"function g({a, b}: Opts, tail: number): void", []string{"arg1", "tail"}},
		// Fresh (not-yet-imported) auto-import candidate: detail leads with an
		// "Auto import from '...'" preamble line before the real signature.
		{"Auto import from './helper'\nfunction bar(x: number, y: number): number", []string{"x", "y"}},
		{"Auto import from './helper'\nfunction now(): number", nil},
	}
	for _, tc := range cases {
		got := parseSignatureParams(tc.detail)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("parseSignatureParams(%q) = %#v, want %#v", tc.detail, got, tc.want)
		}
	}
}

// TestSnippetModeArgPlaceholders drives the interactive snippet flow: accepting
// a function enters snippet mode with the first argument selected; Tab jumps to
// the next; typing over a placeholder replaces it and repositions the remaining
// stops.
func TestSnippetModeArgPlaceholders(t *testing.T) {
	m := newCompletionApplyTestModel("gr\n")
	at := document.Pos{Line: 0, Col: 2}
	m.cursor = at
	item := ClientCompletion{
		Label: "greet", InsertText: "greet", Kind: 3,
		Detail: "function greet(name: string, times: number): void",
	}
	res, _ := m.applyCompletionItem(item, at, "gr")
	m = res.(Model)

	if !m.snippetOn || len(m.snippetStops) != 3 {
		t.Fatalf("snippetOn=%v stops=%d, want true and 3 stops (2 args + final)", m.snippetOn, len(m.snippetStops))
	}
	if m.buf.Line(0) != "greet(name, times)" {
		t.Fatalf("inserted line = %q, want greet(name, times)", m.buf.Line(0))
	}
	if m.sel == nil || m.sel.Anchor.Col != 6 || m.sel.Head.Col != 9 {
		t.Fatalf("first placeholder not selected: sel=%+v", m.sel)
	}

	// Tab selects the second placeholder ("times" at cols 12..16 inclusive).
	m2 := m.snippetJump(1).(Model)
	if m2.sel == nil || m2.sel.Anchor.Col != 12 || m2.sel.Head.Col != 16 {
		t.Fatalf("second placeholder not selected after Tab: sel=%+v", m2.sel)
	}

	// Typing over the first placeholder replaces it and shifts the second stop
	// left by len("name")-len("x") = 3.
	res2, _ := m.snippetEdit(fakeKey("x"))
	m3 := res2.(Model)
	if m3.buf.Line(0) != "greet(x, times)" {
		t.Errorf("after replace: line = %q, want greet(x, times)", m3.buf.Line(0))
	}
	if m3.snippetStops[1].start != 9 {
		t.Errorf("second stop start = %d, want 9 (shifted by -3)", m3.snippetStops[1].start)
	}
}

// TestSnippetEscLeavesInsertMode verifies a single Esc exits snippet mode AND
// insert mode (vim-consistent), rather than requiring two presses.
func TestSnippetEscLeavesInsertMode(t *testing.T) {
	m := newCompletionApplyTestModel("greet(name)\n")
	m.cursor = document.Pos{Line: 0, Col: 6}
	m = m.enterSnippet(0, []snippetStop{{6, 10}, {11, 11}})
	if !m.snippetOn {
		t.Fatal("setup: expected snippet mode on")
	}

	res, _ := m.handleInsert(fakeKey("esc"))
	got := res.(Model)
	if got.snippetOn {
		t.Error("snippet mode still on after Esc")
	}
	if got.mode != ModeNormal {
		t.Errorf("mode = %v after Esc, want ModeNormal (single Esc should leave insert mode)", got.mode)
	}
}

// TestSnippetEditDoesNotAutoTriggerCompletion is a regression test: typing a
// value into an argument placeholder (e.g. replacing "y" with "1") must not
// schedule an auto-triggered completion fetch. If it did, the resulting popup
// would outrank snippet mode in handleInsert, stealing Tab from argument
// navigation instead of jumping to the next placeholder.
func TestSnippetEditDoesNotAutoTriggerCompletion(t *testing.T) {
	m := newCompletionApplyTestModel("bar(x, y)\n")
	m.cursor = document.Pos{Line: 0, Col: 4}
	m = m.enterSnippet(0, []snippetStop{{4, 5}, {7, 8}, {9, 9}})
	m = m.selectSnippetStop(1) // select "y"

	res, _ := m.snippetEdit(fakeKey("1"))
	got := res.(Model)

	if got.buf.Line(0) != "bar(x, 1)" {
		t.Fatalf("line 0 = %q, want bar(x, 1)", got.buf.Line(0))
	}
	if got.completionSeq != m.completionSeq {
		t.Error("typing into a placeholder scheduled an auto-trigger; it should not")
	}
	if got.completionOn {
		t.Error("completion popup should not be active during snippet editing")
	}

	// Tab must still jump stops normally, not be hijacked by a completion popup:
	// one Tab reaches the final (empty, past ')') stop, still in snippet mode...
	atFinal := got.snippetJump(1).(Model)
	if !atFinal.snippetOn {
		t.Fatal("Tab should land on the final stop, still in snippet mode")
	}
	// ...and one more Tab past it exits snippet mode.
	done := atFinal.snippetJump(1).(Model)
	if done.snippetOn {
		t.Error("Tab past the last stop should exit snippet mode")
	}
}

// TestMouseClickDuringSnippetExitsSnippetMode is a regression test: a mouse
// click moves the cursor without going through handleInsert's key dispatch, so
// unlike every keyboard-driven cursor move it didn't trigger exitSnippet() —
// leaving stale snippet stops active for what was now an unrelated cursor
// position. A subsequent Tab would snap the cursor back to the old snippet
// line/columns instead of respecting the click.
func TestMouseClickDuringSnippetExitsSnippetMode(t *testing.T) {
	m := newCompletionApplyTestModel("greet(name, times)\nother line\n")
	m = m.enterSnippet(0, []snippetStop{{6, 10}, {12, 17}, {18, 18}})
	if !m.snippetOn || m.sel == nil {
		t.Fatal("setup: expected snippet mode active with a placeholder selected")
	}

	m.handleMousePress(0, 1) // click onto the second line, away from the snippet

	if m.snippetOn {
		t.Error("snippet mode should be exited by a mouse click elsewhere")
	}
	want := document.Pos{Line: 1, Col: 0}
	// The click sets its own fresh single-point selection (normal click
	// behavior) — the bug is a stale *placeholder* selection surviving, not
	// selection-in-general, so assert the selection is anchored at the click,
	// not left over from the snippet.
	if m.sel == nil || m.sel.Anchor != want || m.sel.Head != want {
		t.Errorf("sel = %+v, want a fresh point selection at %v (not the stale placeholder)", m.sel, want)
	}
	if m.cursor != want {
		t.Errorf("cursor = %v, want %v (the clicked position, not snapped back to the snippet)", m.cursor, want)
	}
}
