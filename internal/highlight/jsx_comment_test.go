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
// ({/* */}) and which take a JavaScript one (//). alone is the answer for the
// line selected by itself; inRange is the answer when the whole component is
// selected, where a child spanning lines is taken whole from its first line.
func TestJSXChildLines(t *testing.T) {
	h := New("test.tsx")
	if h == nil {
		t.Skip("tsx grammar not compiled in (run with -tags lang_all)")
	}
	cases := []struct {
		line           int
		alone, inRange bool
		what           string
	}{
		{0, false, false, "function declaration"},
		{1, false, false, "statement"},
		{2, false, false, "return ("},
		{3, false, false, "outermost opening tag"},
		{4, true, true, "nested element"},
		{5, true, true, "text child"},
		{6, true, true, "{expression} child"},
		{7, false, true, "element inside a {expression}'s JavaScript"},
		{8, false, true, "closing of a {expression}"},
		{9, true, true, "self-closing element child, multi-line"},
		{10, false, true, "attribute line inside a child's tag"},
		{11, false, true, "end of a multi-line self-closing child"},
		{12, true, true, "fragment child"},
		{13, true, true, "element inside a fragment"},
		{14, true, true, "fragment's closing tag"},
		{15, false, false, "outermost closing tag"},
		{16, false, false, ");"},
	}
	all := h.JSXChildLines([]byte(jsxSample), 0, 16)
	for _, c := range cases {
		if got := h.JSXChildLines([]byte(jsxSample), c.line, c.line)[0]; got != c.alone {
			t.Errorf("line %d (%s) alone: JSX children = %v, want %v", c.line, c.what, got, c.alone)
		}
		if all[c.line] != c.inRange {
			t.Errorf("line %d (%s) in the whole component: JSX children = %v, want %v", c.line, c.what, all[c.line], c.inRange)
		}
	}
	// A range starting partway into a child reads its lines on their own.
	if sub := h.JSXChildLines([]byte(jsxSample), 10, 12); sub[0] || sub[1] || !sub[2] {
		t.Errorf("JSXChildLines(10, 12) = %v, want [false false true]", sub)
	}
	// Text runs up to the next tag, here the outermost closing tag, which is
	// still JavaScript.
	src := "const a = (\n  <div>\n    text\n  </div>\n);\n"
	if got := h.JSXChildLines([]byte(src), 0, 4); got[3] || !got[2] {
		t.Errorf("text before the outermost closing tag: %v, want only line 2 true", got)
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

// TestJSXFilesParseJSX: .jsx is JavaScript with JSX, so it gets the
// JavaScript grammar and the same children detection as .tsx.
func TestJSXFilesParseJSX(t *testing.T) {
	h := New("component.jsx")
	if h == nil {
		t.Skip("javascript grammar not compiled in (run with -tags lang_all)")
	}
	if got := h.JSXChildLines([]byte("const a = (\n  <div>\n    text\n  </div>\n);\n"), 2, 2); !got[0] {
		t.Error("a .jsx text child was not reported as JSX children")
	}
}
