package client

import (
	"reflect"
	"testing"

	"github.com/indiejames/indigo/internal/document"
)

func TestSplitCaseWords(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"my_variable_name", []string{"my", "variable", "name"}},
		{"MY_CONST_NAME", []string{"MY", "CONST", "NAME"}},
		{"my-kebab-var", []string{"my", "kebab", "var"}},
		{"my.dot.var", []string{"my", "dot", "var"}},
		{"myVariableName", []string{"my", "Variable", "Name"}},
		{"MyVariableName", []string{"My", "Variable", "Name"}},
		{"myHTTPServer", []string{"my", "HTTP", "Server"}},
		{"XMLParser", []string{"XML", "Parser"}},
		{"value2Name", []string{"value2", "Name"}},
		{"ID", []string{"ID"}},
		{"", nil},
	}
	for _, c := range cases {
		got := splitCaseWords(c.in)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("splitCaseWords(%q) = %#v, want %#v", c.in, got, c.want)
		}
	}
}

func TestCaseJoiners(t *testing.T) {
	words := []string{"my", "HTTP", "server"}
	cases := []struct {
		name string
		join func([]string) string
		want string
	}{
		{"snake", joinSnakeCase, "my_http_server"},
		{"screaming_snake", joinScreamingSnakeCase, "MY_HTTP_SERVER"},
		{"kebab", joinKebabCase, "my-http-server"},
		{"dot", joinDotCase, "my.http.server"},
		{"camel", joinCamelCase, "myHttpServer"},
		{"pascal", joinPascalCase, "MyHttpServer"},
	}
	for _, c := range cases {
		got := c.join(words)
		if got != c.want {
			t.Errorf("%s(%v) = %q, want %q", c.name, words, got, c.want)
		}
	}
}

// TestExecuteCaseConvertNoSelectionUsesEnclosingIdentifier is a regression
// test: with no selection, ~s must convert the whole identifier the cursor
// sits inside, not just the character under it.
func TestExecuteCaseConvertNoSelectionUsesEnclosingIdentifier(t *testing.T) {
	m := newTestModel("myVariableName\n")
	m.rpc = &RPC{}
	m.cursor = document.Pos{Line: 0, Col: 5} // inside "Variable"

	m2, _ := executeCaseConvertSnake(m)
	got := m2.(Model)

	if got.buf.Line(0) != "my_variable_name" {
		t.Errorf("line = %q, want %q", got.buf.Line(0), "my_variable_name")
	}
}

func TestExecuteCaseConvertOnExplicitSelection(t *testing.T) {
	m := newTestModel("prefix myVariableName suffix\n")
	m.rpc = &RPC{}
	m.sel = &Selection{Anchor: document.Pos{Line: 0, Col: 7}, Head: document.Pos{Line: 0, Col: 20}}

	m2, _ := executeCaseConvertKebab(m)
	got := m2.(Model)

	want := "prefix my-variable-name suffix"
	if got.buf.Line(0) != want {
		t.Errorf("line = %q, want %q", got.buf.Line(0), want)
	}
	if got.sel != nil {
		t.Errorf("sel = %v, want nil", got.sel)
	}
}

// TestExecuteCaseConvertAllCursors is a regression test: case conversion
// must apply to every cursor, not just the primary one.
func TestExecuteCaseConvertAllCursors(t *testing.T) {
	m := newTestModel("myFirstVar\nmySecondVar\n")
	m.rpc = &RPC{}
	m.cursor = document.Pos{Line: 0, Col: 3}
	m.extraCursors = []ExtraCursor{{pos: document.Pos{Line: 1, Col: 3}}}

	m2, _ := executeCaseConvertSnake(m)
	got := m2.(Model)

	if got.buf.Line(0) != "my_first_var" {
		t.Errorf("line 0 = %q, want %q", got.buf.Line(0), "my_first_var")
	}
	if got.buf.Line(1) != "my_second_var" {
		t.Errorf("line 1 = %q, want %q", got.buf.Line(1), "my_second_var")
	}
}

// TestExecuteCaseConvertNoIdentifierIsNoop is a regression test: invoking
// case-convert with the cursor on whitespace (no selection, no enclosing
// identifier) must not modify the buffer.
func TestExecuteCaseConvertNoIdentifierIsNoop(t *testing.T) {
	m := newTestModel("foo   bar\n")
	m.rpc = &RPC{}
	m.cursor = document.Pos{Line: 0, Col: 4}

	m2, _ := executeCaseConvertSnake(m)
	got := m2.(Model)

	if got.buf.Line(0) != "foo   bar" {
		t.Errorf("line = %q, want unchanged %q", got.buf.Line(0), "foo   bar")
	}
}

func TestFindIdentifierAt(t *testing.T) {
	runes := []rune("foo my-var.dot bar")
	start, end, ok := findIdentifierAt(runes, 6)
	if !ok || start != 4 || end != 13 {
		t.Errorf("findIdentifierAt = (%d, %d, %v), want (4, 13, true)", start, end, ok)
	}
	if string(runes[start:end+1]) != "my-var.dot" {
		t.Errorf("span text = %q, want %q", string(runes[start:end+1]), "my-var.dot")
	}

	_, _, ok = findIdentifierAt(runes, 3) // space
	if ok {
		t.Error("findIdentifierAt on whitespace should return ok=false")
	}
}

// TestExecuteCaseConvertMultipleCursorsSameLine is a regression test: when
// multiple cursors are on the same line and processed back-to-front, a
// conversion that changes the length of the text to the left of an
// already-processed cursor must adjust that cursor's saved position so it
// points to the correct column after all edits complete.
func TestExecuteCaseConvertMultipleCursorsSameLine(t *testing.T) {
	// Line with two identifiers where converting the right one to snake_case
	// makes it longer, then converting the left one affects positions.
	// Using identifiers that change length when converted:
	// "myVar" (5 chars) -> "my_var" (6 chars, +1)
	// "aB" (2 chars) -> "a_b" (3 chars, +1)
	m := newTestModel("aB myVar\n")
	m.rpc = &RPC{}
	// Place cursors: one at col 0 (on "aB"), one at col 3 (on "myVar")
	m.cursor = document.Pos{Line: 0, Col: 3}
	m.extraCursors = []ExtraCursor{{pos: document.Pos{Line: 0, Col: 0}}}

	m2, _ := executeCaseConvertSnake(m)
	got := m2.(Model)

	want := "a_b my_var"
	if got.buf.Line(0) != want {
		t.Errorf("line = %q, want %q", got.buf.Line(0), want)
	}

	// Check that both cursors are positioned correctly after their conversions.
	// The conversion processes back-to-front: myVar first (becomes my_var at col 4),
	// then aB (becomes a_b at col 0). When aB is converted, it grows by 1 char,
	// so the saved cursor from myVar conversion should be adjusted.
	// After "myVar" -> "my_var" conversion (delete cols 3-7, insert at col 3),
	// cursor remains at col 3 (start of converted text).
	// After "aB" -> "a_b" (delta +1), that saved cursor should shift to col 4.
	cursors := []document.Pos{got.cursor}
	for _, ec := range got.extraCursors {
		cursors = append(cursors, ec.pos)
	}

	// We expect two cursors, both on line 0.
	if len(cursors) != 2 {
		t.Fatalf("got %d cursors, want 2", len(cursors))
	}

	// Sort by column to check them in order
	if cursors[0].Col > cursors[1].Col {
		cursors[0], cursors[1] = cursors[1], cursors[0]
	}

	// First cursor: after "a_b" conversion, should be at start of "a_b" (col 0)
	if cursors[0].Line != 0 || cursors[0].Col != 0 {
		t.Errorf("left cursor = %v, want {Line: 0, Col: 0}", cursors[0])
	}

	// Second cursor: after "my_var" conversion at original position,
	// would be at col 3 (start of "my_var" at original position 3, now position 4).
	// After left edit adds 1 char, should be at col 4.
	if cursors[1].Line != 0 || cursors[1].Col != 4 {
		t.Errorf("right cursor = %v, want {Line: 0, Col: 4}", cursors[1])
	}
}
