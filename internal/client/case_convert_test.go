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
