package highlight

import "testing"

const jsxSample = `function App() {
  const x = 1;
  return (
    <div className="app">
      <h1>Title</h1>
      plain text
      {items.map(i =>
        <li>{i}</li>
      )}
      <Foo
        bar="baz"
      />
      <>
        <span />
      </>
    </div>
  );
}
`

// TestJSXChildLines pins which lines of a component take a JSX comment
// ({/* */}) and which take a JavaScript one (//).
func TestJSXChildLines(t *testing.T) {
	h := New("test.tsx")
	if h == nil {
		t.Skip("tsx grammar not compiled in (run with -tags lang_all)")
	}
	cases := []struct {
		line int
		want bool
		what string
	}{
		{0, false, "function declaration"},
		{1, false, "statement"},
		{2, false, "return ("},
		{3, false, "outermost opening tag"},
		{4, true, "nested element"},
		{5, true, "text child"},
		{6, true, "{expression} child"},
		{7, false, "element inside a {expression}'s JavaScript"},
		{8, false, "closing of a {expression}"},
		{9, true, "self-closing element child, multi-line"},
		{10, false, "attribute line inside a tag"},
		{11, false, "end of a multi-line self-closing tag"},
		{12, true, "fragment child"},
		{13, true, "element inside a fragment"},
		{14, true, "fragment's closing tag"},
		{15, false, "outermost closing tag"},
		{16, false, ");"},
	}
	got := h.JSXChildLines([]byte(jsxSample), 0, 16)
	for _, c := range cases {
		if got[c.line] != c.want {
			t.Errorf("line %d (%s): JSX children = %v, want %v", c.line, c.what, got[c.line], c.want)
		}
	}
	// A sub-range answers the same, indexed from its own start.
	if sub := h.JSXChildLines([]byte(jsxSample), 4, 5); len(sub) != 2 || !sub[0] || !sub[1] {
		t.Errorf("JSXChildLines(4, 5) = %v, want [true true]", sub)
	}
}

func TestJSXChildLinesIsFalseWithoutJSX(t *testing.T) {
	if h := New("test.go"); h != nil && h.JSXChildLines([]byte("package x\n\nvar y = 1\n"), 2, 2)[0] {
		t.Error("a Go line reported as JSX children")
	}
	var h *Highlighter
	if got := h.JSXChildLines([]byte("<div>\n  x\n</div>\n"), 1, 1); len(got) != 1 || got[0] {
		t.Errorf("a nil highlighter answered %v, want [false]", got)
	}
	if got := New("test.tsx").JSXChildLines([]byte("<div>\n  x\n</div>\n"), 0, 9); len(got) != 10 {
		t.Errorf("a range past the end gave %d answers, want 10", len(got))
	}
}
