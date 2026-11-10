package client

import (
	"testing"

	"github.com/indiejames/indigo/internal/document"
	"github.com/indiejames/indigo/internal/highlight"
)

const tsxComponent = `function App() {
  const x = 1;
  return (
    <div>
      <h1>Title</h1>
      text
    </div>
  );
}
`

func newTSXModel(t *testing.T, content string) Model {
	t.Helper()
	m := newTestModel(content)
	m.buf = document.New("app.tsx", content)
	m.filePath = "app.tsx"
	m.hlr = highlight.New("app.tsx")
	if m.hlr == nil {
		t.Skip("tsx grammar not compiled in (run with -tags lang_all)")
	}
	m.rpc = &RPC{}
	return m
}

func toggleLines(m Model, from, to int) Model {
	// Lines from..to, through the break after to.
	m.cursor = document.Pos{Line: to + 1}
	m.sel = &Selection{Anchor: document.Pos{Line: from}, Head: document.Pos{Line: to + 1}}
	got, _ := executeToggleComment(m)
	return got.(Model)
}

// TestToggleCommentUsesJSXCommentsAmongChildren: in a .tsx file a line among
// an element's children is rendered as text, so "//" would show on the page.
// It gets {/* ... */}, while the component's code keeps "//", and toggling
// again restores the original exactly.
func TestToggleCommentUsesJSXCommentsAmongChildren(t *testing.T) {
	m := newTSXModel(t, tsxComponent)

	m = toggleLines(m, 1, 5)
	// The outermost <div> is JavaScript (it follows "return ("), so it gets
	// "//"; only its children get the JSX comment.
	want := `function App() {
  // const x = 1;
  // return (
    // <div>
      {/* <h1>Title</h1> */}
      {/* text */}
    </div>
  );
}
`
	if got := m.buf.Content(); got != want {
		t.Fatalf("after commenting:\n%s\nwant:\n%s", got, want)
	}

	m = toggleLines(m, 1, 5)
	if got := m.buf.Content(); got != tsxComponent {
		t.Fatalf("after uncommenting:\n%s\nwant the original:\n%s", got, tsxComponent)
	}
}

// TestToggleCommentTakesAMultiLineChildWhole: a child element whose tag spans
// lines is commented {/* */} on every line. A "//" on its attribute or "/>"
// line would sit among the children once its first line is commented, and
// render as text.
func TestToggleCommentTakesAMultiLineChildWhole(t *testing.T) {
	src := "const a = (\n  <div>\n    <Foo\n      bar=\"baz\"\n    />\n  </div>\n);\n"
	m := newTSXModel(t, src)
	m = toggleLines(m, 2, 4)
	want := "const a = (\n  <div>\n    {/* <Foo */}\n      {/* bar=\"baz\" */}\n    {/* /> */}\n  </div>\n);\n"
	if got := m.buf.Content(); got != want {
		t.Fatalf("after commenting:\n%s\nwant:\n%s", got, want)
	}
	m = toggleLines(m, 2, 4)
	if got := m.buf.Content(); got != src {
		t.Fatalf("after uncommenting:\n%s\nwant the original:\n%s", got, src)
	}
}

// TestToggleCommentUncommentsAJSXLineCommentedWithSlashes: a children line
// commented with "//" before the toggle understood JSX still uncomments.
func TestToggleCommentUncommentsAJSXLineCommentedWithSlashes(t *testing.T) {
	m := newTSXModel(t, "const a = (\n  <div>\n    // text\n  </div>\n);\n")
	m = toggleLines(m, 2, 2)
	if got, want := m.buf.Content(), "const a = (\n  <div>\n    text\n  </div>\n);\n"; got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

// TestToggleCommentLeavesBracesInOtherLanguagesAlone: "{/* x */}" means
// nothing special outside JSX, so in Go it is commented like any other line.
func TestToggleCommentLeavesBracesInOtherLanguagesAlone(t *testing.T) {
	m := newTestModel("{/* x */}\n")
	m.filePath = "test.go"
	m.rpc = &RPC{}
	m.hlr = highlight.New("test.go")
	m = toggleLines(m, 0, 0)
	if got, want := m.buf.Content(), "// {/* x */}\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
