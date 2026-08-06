package client

import (
	"testing"

	"github.com/indiejames/indigo/internal/document"
)

func TestParseSubstitute(t *testing.T) {
	cases := []struct {
		rest        string
		wantPattern string
		wantRepl    string
		wantOK      bool
	}{
		{"/foo/bar/", "foo", "bar", true},
		{`/a\/b/c/`, "a/b", "c", true},
		{`/\d+/N/`, `\d+`, "N", true},
		{"ave", "", "", false},      // "save"
		{"", "", "", false},         // bare "s"
		{"et ft=go", "", "", false}, // "set ft=go"
	}
	for _, c := range cases {
		pattern, repl, ok := parseSubstitute(c.rest)
		if ok != c.wantOK || pattern != c.wantPattern || repl != c.wantRepl {
			t.Errorf("parseSubstitute(%q) = (%q,%q,%v), want (%q,%q,%v)",
				c.rest, pattern, repl, ok, c.wantPattern, c.wantRepl, c.wantOK)
		}
	}
}

func TestExecuteCommandSubstituteWholeBuffer(t *testing.T) {
	m := newTestModel("foo bar\nfoo baz\n")
	m.rpc = &RPC{}
	m.cmdBuf = "s/foo/qux/"
	m2, _ := m.executeCommand()
	got := m2.(Model)
	want := "qux bar\nqux baz\n"
	if got.buf.Content() != want {
		t.Errorf("content = %q, want %q", got.buf.Content(), want)
	}
	if got.status != "2 substitution(s)" {
		t.Errorf("status = %q, want '2 substitution(s)'", got.status)
	}
	if got.mode != ModeNormal {
		t.Errorf("mode = %v, want ModeNormal", got.mode)
	}
}

func TestExecuteCommandSubstituteNoMatch(t *testing.T) {
	m := newTestModel("hello\n")
	m.rpc = &RPC{}
	m.cmdBuf = "s/xyz/abc/"
	m2, _ := m.executeCommand()
	got := m2.(Model)
	if got.status != "E: pattern not found" {
		t.Errorf("status = %q, want 'E: pattern not found'", got.status)
	}
	if got.buf.Content() != "hello\n" {
		t.Errorf("buffer should be unchanged, got %q", got.buf.Content())
	}
}

func TestExecuteCommandSubstituteInvalidRegex(t *testing.T) {
	m := newTestModel("hello\n")
	m.rpc = &RPC{}
	m.cmdBuf = `s/\[unclosed/x/`
	m2, _ := m.executeCommand()
	got := m2.(Model)
	if len(got.status) < 2 || got.status[:2] != "E:" {
		t.Errorf("status = %q, want an E: error", got.status)
	}
	if got.buf.Content() != "hello\n" {
		t.Errorf("buffer should be unchanged, got %q", got.buf.Content())
	}
}

func TestExecuteCommandSubstituteScopedToSelection(t *testing.T) {
	m := newTestModel("foo\nfoo\nfoo\n")
	m.rpc = &RPC{}
	// Linewise selection covering just line 1 (0-based).
	m.sel = &Selection{
		Anchor: document.Pos{Line: 1, Col: 0},
		Head:   document.Pos{Line: 1, Col: 2},
		IsLine: true,
	}
	m.cmdBuf = "s/foo/bar/"
	m2, _ := m.executeCommand()
	got := m2.(Model)
	want := "foo\nbar\nfoo\n"
	if got.buf.Content() != want {
		t.Errorf("content = %q, want %q", got.buf.Content(), want)
	}
	if got.sel != nil {
		t.Errorf("selection should be cleared after substitute, got %+v", got.sel)
	}
}

func TestExecuteCommandSubstituteBackreferences(t *testing.T) {
	m := newTestModel("alice-bob\n")
	m.rpc = &RPC{}
	m.cmdBuf = `s/\(\w+)-(\w+)/$2-$1/`
	m2, _ := m.executeCommand()
	got := m2.(Model)
	want := "bob-alice\n"
	if got.buf.Content() != want {
		t.Errorf("content = %q, want %q", got.buf.Content(), want)
	}
}

func TestExecuteCommandSaveStillWorks(t *testing.T) {
	// Regression: bare "s" and "save" must still trigger save, not substitute
	// — "s/..." is the only form that's ever treated as a substitute.
	m := newTestModel("text")
	m.rpc = &RPC{}
	for _, cmd := range []string{"s", "save"} {
		m.cmdBuf = cmd
		m2, cmd2 := m.executeCommand()
		got := m2.(Model)
		if got.mode != ModeNormal {
			t.Errorf("%q: mode = %v, want ModeNormal", cmd, got.mode)
		}
		if cmd2 == nil {
			t.Errorf("%q: expected a save cmd", cmd)
		}
	}
}
