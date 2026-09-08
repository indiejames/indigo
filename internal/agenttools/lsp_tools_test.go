package agenttools

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/indiejames/indigo/internal/client"
)

// TestResolvePos covers the 1-based-in / 0-based-out conversion and the
// default column. Agents quote positions the way compilers and humans do
// (1-based), while every RPC here is 0-based, so an off-by-one lands the
// query on the wrong token and returns a confidently wrong answer.
func TestResolvePos(t *testing.T) {
	const content = "package main\n\tfunc foo() {}\nlast\n"

	cases := []struct {
		name          string
		in            symbolPosInput
		wantLine, col int
		wantErr       bool
	}{
		{"explicit line and col", symbolPosInput{Line: "1", Col: "9"}, 0, 8, false},
		{"col defaults past the indent", symbolPosInput{Line: "2"}, 1, 1, false},
		{"col defaults to 0 with no indent", symbolPosInput{Line: "3"}, 2, 0, false},
		{"whitespace tolerated", symbolPosInput{Line: " 1 ", Col: " 2 "}, 0, 1, false},
		{"line past end still resolves", symbolPosInput{Line: "99"}, 98, 0, false},
		{"zero line rejected", symbolPosInput{Line: "0"}, 0, 0, true},
		{"negative line rejected", symbolPosInput{Line: "-3"}, 0, 0, true},
		{"non-numeric line rejected", symbolPosInput{Line: "abc"}, 0, 0, true},
		{"zero col rejected", symbolPosInput{Line: "1", Col: "0"}, 0, 0, true},
		{"non-numeric col rejected", symbolPosInput{Line: "1", Col: "x"}, 0, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			line, col, err := resolvePos(content, tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("resolvePos(%+v) = (%d,%d), want an error", tc.in, line, col)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolvePos(%+v): %v", tc.in, err)
			}
			if line != tc.wantLine || col != tc.col {
				t.Errorf("resolvePos(%+v) = (%d,%d), want (%d,%d)", tc.in, line, col, tc.wantLine, tc.col)
			}
		})
	}
}

// TestHasErrorOrWarning covers the gate that decides when the diagnostics
// poll may stop early.
//
// It exists because of a real miss: returning on the *first* diagnostic of any
// kind looked correct, but the spell-check plugin answers almost instantly
// while a compiler error takes seconds, so the poll stopped with a list of
// spelling hints and the type error never appeared in the result.
func TestHasErrorOrWarning(t *testing.T) {
	hints := []client.ClientDiag{
		{Severity: 3, Message: "info"},
		{Severity: 4, Message: "hint"},
	}
	if hasErrorOrWarning(hints) {
		t.Error("info/hint diagnostics should not stop the poll — a compiler error may still be coming")
	}
	if !hasErrorOrWarning(append(hints, client.ClientDiag{Severity: 1, Message: "boom"})) {
		t.Error("an error must stop the poll immediately")
	}
	if !hasErrorOrWarning([]client.ClientDiag{{Severity: 2, Message: "warn"}}) {
		t.Error("a warning must stop the poll immediately")
	}
	if hasErrorOrWarning(nil) {
		t.Error("no diagnostics is not an error")
	}
}

func TestSeverityLabel(t *testing.T) {
	for sev, want := range map[uint8]string{1: "error", 2: "warning", 3: "info", 4: "hint", 9: "hint"} {
		if got := severityLabel(sev); got != want {
			t.Errorf("severityLabel(%d) = %q, want %q", sev, got, want)
		}
	}
}

// TestLSPToolsAreAnnotatedReadOnly pins the read/write split MCP clients use
// to decide what can be allowlisted without a prompt. Getting this backwards
// would let an edit tool through unprompted.
func TestLSPToolsAreAnnotatedReadOnly(t *testing.T) {
	got := map[string]bool{}
	for _, tool := range mcpTools() {
		if tool.Annotations == nil {
			t.Fatalf("%s has no annotations; a client cannot tell whether it mutates", tool.Name)
		}
		got[tool.Name] = tool.Annotations.ReadOnlyHint
	}

	for _, name := range []string{"read_file", "get_diagnostics", "find_definition", "find_references", "list_symbols"} {
		if ro, ok := got[name]; !ok {
			t.Errorf("%s is not exposed over MCP", name)
		} else if !ro {
			t.Errorf("%s must be readOnly: it only queries", name)
		}
	}
	for _, name := range []string{"apply_edits", "insert_at_line", "save_file"} {
		if ro, ok := got[name]; !ok {
			t.Errorf("%s is not exposed over MCP", name)
		} else if ro {
			t.Errorf("%s must NOT be readOnly: it changes the user's code, and marking it "+
				"read-only would let a client run it without prompting", name)
		}
	}
}

// TestLSPToolDescriptionsSayWhyToPreferThem checks the descriptions steer an
// agent away from grep, which is the whole reason these tools exist. A correct
// tool that never gets chosen is worth nothing.
func TestLSPToolDescriptionsSayWhyToPreferThem(t *testing.T) {
	want := map[string]string{
		"find_definition": "grep",
		"find_references": "search",
		"get_diagnostics": "build",
	}
	for _, tool := range AllTools() {
		if kw, ok := want[tool.Name]; ok && !strings.Contains(strings.ToLower(tool.Description), kw) {
			t.Errorf("%s's description never mentions %q, so nothing tells the agent when to "+
				"reach for it instead of the obvious alternative", tool.Name, kw)
		}
	}
}

// TestListSymbolsInfersPathOrExplains covers both halves of how a query-only
// search picks its language server.
//
// LSP's workspace/symbol is answered by a single language server, so something
// has to select which one. The first version opened "." as a stand-in buffer,
// which the server cannot do ("cannot open .: ... is a directory"). The second
// made path required — correct, but it made the tool unreachable from a cold
// session, which knows a symbol name and no paths at all, so grep won every
// time. Today path is optional and representativeFiles infers candidates from
// the workspace; when it cannot, the error has to name the missing input
// rather than fail opaquely.
func TestListSymbolsInfersPathOrExplains(t *testing.T) {
	// An empty directory has nothing to infer from.
	msg, isErr := execListSymbols(t.Context(), nil, t.TempDir(), listSymbolsInput{Query: "Foo"})
	if !isErr {
		t.Fatalf("query in a workspace with no source files returned success (%q); there is "+
			"no way to pick which language server answers", msg)
	}
	if !strings.Contains(msg, "path") {
		t.Errorf("message = %q, want it to point at path as the way out", msg)
	}
	// The schema must agree that path is optional, or an agent that only has a
	// symbol name will skip the tool entirely rather than try it.
	for _, tool := range AllTools() {
		if tool.Name != "list_symbols" {
			continue
		}
		for _, r := range tool.InputSchema.Required {
			if r == "path" {
				t.Error("list_symbols' schema marks path required, so a cold session that " +
					"knows only a symbol name cannot call it and falls back to grep")
			}
		}
	}
}

// TestSymbolNameEntryPointsAreReachableColdStart guards the property that
// makes these tools get used at all.
//
// A session that has just been asked "what calls X?" or "where is X defined?"
// knows a symbol name and nothing else. While these tools required a path, an
// agent could not call them without first finding that path — and the cheapest
// way to find it was grep, which also happened to answer the original question.
// So the LSP tools lost every time, no matter what the descriptions said. Both
// tools must therefore be callable from a bare name.
func TestSymbolNameEntryPointsAreReachableColdStart(t *testing.T) {
	for _, name := range []string{"find_definition", "find_references"} {
		var tool ToolDef
		for _, td := range AllTools() {
			if td.Name == name {
				tool = td
			}
		}
		if tool.Name == "" {
			t.Fatalf("%s is missing from AllTools()", name)
		}
		if _, ok := tool.InputSchema.Properties["symbol"]; !ok {
			t.Errorf("%s takes no symbol argument, so a session holding only a name "+
				"cannot call it and falls back to grep", name)
		}
		for _, r := range tool.InputSchema.Required {
			t.Errorf("%s requires %q; every argument must be optional so a bare "+
				"symbol name is a complete call", name, r)
		}
	}
}

// TestReadFileRangeReplacesSed covers the other half of the same problem:
// pulling a whole large file to see forty lines of it is expensive enough that
// `sed -n '95,140p'` wins, and a shelled-out sed reads stale on-disk content
// rather than the live buffer.
func TestReadFileRangeReplacesSed(t *testing.T) {
	content := "one\ntwo\nthree\nfour\nfive\n"

	// No range: byte-for-byte, because this text comes back as apply_edits'
	// old_text and must stay copyable verbatim.
	if got, isErr := sliceLines(content, readFileInput{Path: "f"}); isErr || got != content {
		t.Errorf("unranged read = %q (isErr=%v), want the content unchanged", got, isErr)
	}

	got, isErr := sliceLines(content, readFileInput{Path: "f", StartLine: 2, EndLine: 4})
	if isErr {
		t.Fatalf("ranged read errored: %s", got)
	}
	if !strings.Contains(got, "two\nthree\nfour") {
		t.Errorf("range 2-4 = %q, want the three lines", got)
	}
	if !strings.Contains(got, "lines 2-4 (of 5)") {
		t.Errorf("range 2-4 = %q, want a header anchoring the line numbers so a "+
			"finding can be cited as file:line without counting", got)
	}
	if strings.Contains(got, "five") {
		t.Errorf("range 2-4 leaked line 5: %q", got)
	}

	// Open-ended and over-long ends clamp rather than erroring: an agent
	// guessing "the rest of the file" should get it.
	if got, isErr := sliceLines(content, readFileInput{Path: "f", StartLine: 4}); isErr ||
		!strings.Contains(got, "four\nfive") {
		t.Errorf("open-ended range = %q (isErr=%v), want lines 4-5", got, isErr)
	}
	if got, isErr := sliceLines(content, readFileInput{Path: "f", StartLine: 2, EndLine: 999}); isErr ||
		!strings.Contains(got, "lines 2-5 (of 5)") {
		t.Errorf("over-long end = %q (isErr=%v), want a clamp to 5", got, isErr)
	}

	// A start past the end is a mistake worth reporting, not an empty answer
	// that reads as "nothing there".
	if got, isErr := sliceLines(content, readFileInput{Path: "f", StartLine: 99}); !isErr ||
		!strings.Contains(got, "past the end") {
		t.Errorf("start past EOF = %q (isErr=%v), want an explanatory error", got, isErr)
	}
}

// TestOffsetToLineColCountsRunes is a regression test for apply_edits
// corrupting any line containing multibyte text.
//
// strings.Index returns a byte offset, but document.Buffer addresses positions
// logically — logicalOffset does lineStart+col over a rune sequence — so a
// column counted in bytes points somewhere else entirely once a line holds
// anything outside ASCII, and the delete op lands on the wrong characters.
func TestOffsetToLineColCountsRunes(t *testing.T) {
	const content = "héllo world\nsecond é line\n"

	// "world" begins after 6 runes ("héllo ") but 7 bytes: é is two bytes.
	idx := strings.Index(content, "world")
	line, col := offsetToLineCol(content, idx)
	if line != 0 || col != 6 {
		t.Errorf("start of \"world\" = line %d col %d, want 0/6 — a byte column would say 7 "+
			"and the edit would land one character late", line, col)
	}
	if _, endCol := offsetToLineCol(content, idx+len("world")); endCol != 11 {
		t.Errorf("end of \"world\" = col %d, want 11", endCol)
	}

	// Columns reset per line, and the multibyte rune on line 2 counts once.
	idx2 := strings.Index(content, "line")
	line2, col2 := offsetToLineCol(content, idx2)
	if line2 != 1 || col2 != 9 {
		t.Errorf("start of \"line\" = line %d col %d, want 1/9", line2, col2)
	}

	// Offsets past the end clamp rather than panicking on the slice.
	if _, _ = offsetToLineCol(content, len(content)+50); false {
		t.Fatal("unreachable")
	}
}

// TestSearchFilesTreatsLeadingDashAsPattern is a regression test for `git grep`
// parsing a pattern that starts with "-" as an option. Without -e, searching
// for a flag name fails with a git usage error instead of searching for it.
func TestSearchFilesTreatsLeadingDashAsPattern(t *testing.T) {
	dir := t.TempDir()
	if err := exec.Command("git", "-C", dir, "init", "-q").Run(); err != nil {
		t.Skipf("git unavailable: %v", err)
	}
	body := "run with --force to skip\nnothing here\n"
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	// git grep only searches tracked files unless told otherwise; add it.
	if err := exec.Command("git", "-C", dir, "add", "notes.txt").Run(); err != nil {
		t.Fatalf("git add: %v", err)
	}

	out, isErr := execSearchFiles(dir, "--force", "", "")
	if isErr {
		t.Fatalf("search for a dash-leading pattern errored: %s", out)
	}
	if !strings.Contains(out, "--force") {
		t.Errorf("search output = %q, want the matching line — git parsed the pattern as an "+
			"option instead of a search expression", out)
	}
}
