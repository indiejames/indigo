package client

import "testing"

func TestDetectExtractedNameFunctionExtraction(t *testing.T) {
	oldContent := "package main\n\nfunc example() {\n\tx := 1\n\ty := 2\n\tz := x + y\n\tfmt.Println(z)\n}\n"
	newText := "func example() {\n\tz := newFunction()\n\tfmt.Println(z)\n}\n\nfunc newFunction() int {\n\tx := 1\n\ty := 2\n\tz := x + y\n\treturn z\n}"

	got := detectExtractedName(oldContent, newText)
	if got != "newFunction" {
		t.Errorf("detectExtractedName() = %q, want %q", got, "newFunction")
	}
}

func TestDetectExtractedNameNoRepeatedNewIdentifier(t *testing.T) {
	// A plain quick-fix (e.g. "remove unused import") introduces no new
	// repeated identifier — must not trigger a rename.
	oldContent := "package main\n\nimport \"fmt\"\n"
	newText := "\n"
	if got := detectExtractedName(oldContent, newText); got != "" {
		t.Errorf("detectExtractedName() = %q, want \"\" (no candidate)", got)
	}
}

func TestDetectExtractedNameAmbiguousReturnsEmpty(t *testing.T) {
	oldContent := "package main\n"
	newText := "foo := bar()\nbaz := qux()\nfoo = baz\nbar = qux\n" // foo, bar, baz, qux all repeat
	if got := detectExtractedName(oldContent, newText); got != "" {
		t.Errorf("detectExtractedName() = %q, want \"\" (ambiguous)", got)
	}
}

func TestDetectExtractedNameIgnoresPreexistingIdentifiers(t *testing.T) {
	oldContent := "func helper() {}\n"
	newText := "helper()\nhelper()\n" // repeated, but already existed before the edit
	if got := detectExtractedName(oldContent, newText); got != "" {
		t.Errorf("detectExtractedName() = %q, want \"\" (not new)", got)
	}
}

func TestFindWholeWordOccurrences(t *testing.T) {
	m := newTestModel("newFunction()\nx := newFunctionCall\nfoo(newFunction)\n")

	got := findWholeWordOccurrences(m, "newFunction")
	want := []struct{ line, col int }{
		{0, 0},
		{2, 4},
	}
	if len(got) != len(want) {
		t.Fatalf("findWholeWordOccurrences() = %v, want %d matches (not matching inside newFunctionCall)", got, len(want))
	}
	for i, w := range want {
		if got[i].Line != w.line || got[i].Col != w.col {
			t.Errorf("match[%d] = (%d,%d), want (%d,%d)", i, got[i].Line, got[i].Col, w.line, w.col)
		}
	}
}

func TestDefaultExtractedName(t *testing.T) {
	cases := []struct {
		kind string
		want string
	}{
		{"refactor.extract.function", "newFunction"},
		{"refactor.extract.method", "newMethod"},
		{"refactor.extract.variable", "newVar"},
		{"refactor.extract.constant", "newVar"},
		{"refactor.rewrite", ""},
		{"quickfix", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := defaultExtractedName(c.kind); got != c.want {
			t.Errorf("defaultExtractedName(%q) = %q, want %q", c.kind, got, c.want)
		}
	}
}

func TestStartExtractRenamePromptEntersCommandMode(t *testing.T) {
	m := newTestModel("func example() {}\n")
	edits := []ClientLspEdit{{FromLine: 0, FromCol: 0, ToLine: 0, ToCol: 18, NewText: "func newFunction() {}"}}

	m2, cmd := startExtractRenamePrompt(m, edits, "refactor.extract.function")

	if m2.mode != ModeCommand {
		t.Errorf("mode = %v, want ModeCommand", m2.mode)
	}
	if m2.cmdBuf != "extract-rename " {
		t.Errorf("cmdBuf = %q, want %q", m2.cmdBuf, "extract-rename ")
	}
	if m2.pendingExtract == nil {
		t.Fatal("pendingExtract should be set")
	}
	if len(m2.pendingExtract.edits) != 1 || m2.pendingExtract.kind != "refactor.extract.function" {
		t.Errorf("pendingExtract = %+v, want the given edits/kind", m2.pendingExtract)
	}
	// No buffer change yet -- the user hasn't typed a name.
	if got := m2.buf.Content(); got != "func example() {}\n" {
		t.Errorf("content changed before a name was submitted: %q", got)
	}
	if cmd != nil {
		t.Error("expected nil cmd -- just entering the prompt")
	}
}

// TestDetectExtractedNameMethodWithControlFlowVariable reproduces a real
// bug: gopls's "Extract method" on a block with multiple early returns
// (e.g. a select inside a for loop) introduces *two* new repeated
// identifiers — the control-flow variable ("shouldReturn") it invents to
// let the extracted method signal "the caller should return", and the
// method name itself. The old repeated-identifier heuristic saw two new
// candidates and bailed out as ambiguous, silently falling through to a
// wrong hardcoded default name, which then pointed the follow-up rename at
// a bogus, unrelated position elsewhere in a large file — LSP rejected it
// with "column is beyond end of line". Detecting via the func declaration
// itself sidesteps the ambiguity entirely. oldContent/newText below are
// trimmed from an actual gopls response for extracting the body of
// internal/server/server.go's watchLoop select statement into a method.
func TestDetectExtractedNameMethodWithControlFlowVariable(t *testing.T) {
	oldContent := `func (s *editorService) watchLoop() {
	for {
		select {
		case event, ok := <-s.watcher.Events:
			if !ok {
				return
			}
			serverLog("watchLoop: event=%s name=%q", event.Op, event.Name)
			if event.Has(fsnotify.Write) || event.Has(fsnotify.Create) || event.Has(fsnotify.Rename) {
				s.handleExternalWrite(event.Name)
			}
		case err, ok := <-s.watcher.Errors:
			if !ok {
				return
			}
			serverLog("watchLoop: error=%v", err)
		}
	}
}`
	newText := `func (s *editorService) watchLoop() {
	for {
		shouldReturn := s.newMethod()
		if shouldReturn {
			return
		}
	}
}

func (s *editorService) newMethod() bool {
	select {
	case event, ok := <-s.watcher.Events:
		if !ok {
			return true
		}
		serverLog("watchLoop: event=%s name=%q", event.Op, event.Name)
		if event.Has(fsnotify.Write) || event.Has(fsnotify.Create) || event.Has(fsnotify.Rename) {
			s.handleExternalWrite(event.Name)
		}
	case err, ok := <-s.watcher.Errors:
		if !ok {
			return true
		}
		serverLog("watchLoop: error=%v", err)
	}
	return false
}`

	got := detectExtractedName(oldContent, newText)
	if got != "newMethod" {
		t.Errorf("detectExtractedName() = %q, want %q", got, "newMethod")
	}
}

// TestDetectExtractedNameBareFunctionWithControlFlowVariable is the
// refactor.extract.function sibling of the above (no receiver): gopls
// names the extracted symbol "newFunction" and passes the receiver in as a
// plain parameter instead of a method receiver.
func TestDetectExtractedNameBareFunctionWithControlFlowVariable(t *testing.T) {
	newText := `func (s *editorService) watchLoop() {
	for {
		shouldReturn := newFunction(s)
		if shouldReturn {
			return
		}
	}
}

func newFunction(s *editorService) bool {
	select {
	case event, ok := <-s.watcher.Events:
		if !ok {
			return true
		}
	case err, ok := <-s.watcher.Errors:
		if !ok {
			return true
		}
	}
	return false
}`

	got := detectExtractedName("", newText)
	if got != "newFunction" {
		t.Errorf("detectExtractedName() = %q, want %q", got, "newFunction")
	}
}
