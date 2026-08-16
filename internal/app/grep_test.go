package app

import (
	"os"
	"path/filepath"
	"testing"
)

// writeTree creates a temporary directory tree for testing.
// files maps relative path → content.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, content := range files {
		abs := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestSearchWorkspaceLiteral(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"a.go": "package main\nfunc hello() {}\n",
		"b.go": "package main\nfunc world() {}\n",
	})
	results, err := searchWorkspace(dir, "hello", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Line != 1 {
		t.Errorf("line = %d, want 1", results[0].Line)
	}
}

func TestSearchWorkspaceCaseInsensitive(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"a.txt": "Hello World\nhello again\n",
	})
	results, err := searchWorkspace(dir, "hello", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results (case-insensitive), got %d", len(results))
	}
}

func TestSearchWorkspaceCaseSensitive(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"a.txt": "Hello World\nhello again\n",
	})
	results, err := searchWorkspace(dir, "Hello", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result (case-sensitive), got %d", len(results))
	}
	if results[0].Line != 0 {
		t.Errorf("line = %d, want 0", results[0].Line)
	}
}

func TestSearchWorkspaceRegex(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"a.go": "func foo() {}\nfunc bar() {}\nvar x = 1\n",
	})
	// \func [a-z]+ → expr: func [a-z]+
	results, err := searchWorkspace(dir, `\func [a-z]+`, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("regex: expected 2 results, got %d", len(results))
	}
}

func TestSearchWorkspaceInvalidRegex(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"a.txt": "hello\n",
	})
	_, err := searchWorkspace(dir, `\[unclosed`, "", "")
	if err == nil {
		t.Error("expected error for invalid regex, got nil")
	}
}

func TestSearchWorkspaceIgnoresDirs(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"main.go":             "func hello() {}\n",
		".git/config":         "hello git\n",
		"vendor/lib/lib.go":   "func hello() {}\n",
		"node_modules/x/x.js": "hello()\n",
	})
	results, err := searchWorkspace(dir, "hello", "", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range results {
		if r.RelPath != "main.go" {
			t.Errorf("unexpected result in ignored dir: %s", r.RelPath)
		}
	}
	if len(results) != 1 {
		t.Errorf("expected 1 result (from main.go only), got %d", len(results))
	}
}

func TestSearchWorkspaceMultipleFiles(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"x/a.go": "TODO: fix this\n",
		"x/b.go": "nothing here\n",
		"y/c.go": "TODO: and this\n",
	})
	results, err := searchWorkspace(dir, "TODO", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 TODO results, got %d", len(results))
	}
}

func TestSearchWorkspaceEmptyPattern(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"a.txt": "hello\n",
	})
	results, err := searchWorkspace(dir, "", "", "")
	if err != nil || results != nil {
		t.Errorf("empty pattern: expected nil,nil got %v,%v", results, err)
	}
}

func TestSearchWorkspaceGlob(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"main.go":   "func hello() {}\n",
		"readme.md": "hello world\n",
		"sub/a.go":  "// hello\n",
	})
	// Only .go files — should exclude readme.md.
	results, err := searchWorkspace(dir, "hello", "*.go", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range results {
		if filepath.Ext(r.RelPath) != ".go" {
			t.Errorf("glob *.go returned non-go file: %s", r.RelPath)
		}
	}
	if len(results) != 2 {
		t.Errorf("expected 2 results (*.go), got %d", len(results))
	}
}

func TestSearchWorkspaceExclude(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"main.go":      "func hello() {}\n",
		"main_test.go": "func hello() {}\n",
		"sub/a.go":     "// hello\n",
	})
	results, err := searchWorkspace(dir, "hello", "", "*_test.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range results {
		if r.RelPath == "main_test.go" {
			t.Errorf("exclude *_test.go still returned excluded file: %s", r.RelPath)
		}
	}
	if len(results) != 2 {
		t.Errorf("expected 2 results (excluding main_test.go), got %d: %+v", len(results), results)
	}
}

func TestSearchWorkspaceIncludeAndExclude(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"main.go":      "hello\n",
		"main_test.go": "hello\n",
		"readme.md":    "hello\n",
	})
	results, err := searchWorkspace(dir, "hello", "*.go", "*_test.go")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].RelPath != "main.go" {
		t.Errorf("expected only main.go, got %+v", results)
	}
}

func TestSplitGlobs(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"*.go", []string{"*.go"}},
		{"*.go,*.ts", []string{"*.go", "*.ts"}},
		{"*.go *.ts", []string{"*.go", "*.ts"}},
		{"*.go, *.ts , vendor/", []string{"*.go", "*.ts", "vendor/"}},
	}
	for _, c := range cases {
		got := splitGlobs(c.in)
		if len(got) != len(c.want) {
			t.Errorf("splitGlobs(%q) = %v, want %v", c.in, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("splitGlobs(%q) = %v, want %v", c.in, got, c.want)
				break
			}
		}
	}
}

func TestMatchGlobs(t *testing.T) {
	cases := []struct {
		includes, excludes []string
		path               string
		want               bool
	}{
		{nil, nil, "main.go", true},
		{[]string{"*.go"}, nil, "main.go", true},
		{[]string{"*.go"}, nil, "readme.md", false},
		{nil, []string{"vendor/"}, "vendor/lib.go", false},
		{nil, []string{"vendor/"}, "main.go", true},
		{[]string{"*.go"}, []string{"*_test.go"}, "main_test.go", false},
		{[]string{"*.go"}, []string{"*_test.go"}, "main.go", true},
	}
	for _, c := range cases {
		got := matchGlobs(c.includes, c.excludes, c.path)
		if got != c.want {
			t.Errorf("matchGlobs(%v, %v, %q) = %v, want %v", c.includes, c.excludes, c.path, got, c.want)
		}
	}
}

// TestGrepResultsMsgDiscardsStaleSequence is a regression test: before
// grepResultsMsg carried a sequence token, a slow older grep request's
// results arriving after a newer one's would silently overwrite the fresher
// results — e.g. searching "foo" then quickly retyping "bar" could still
// leave "foo"'s matches showing if "foo"'s search happened to take longer.
func TestGrepResultsMsgDiscardsStaleSequence(t *testing.T) {
	a := App{grep: &grepPicker{searching: true, seq: 2}}

	// A result for the superseded request (seq 1) must not be applied.
	updated, _ := a.Update(grepResultsMsg{seq: 1, results: []GrepResult{{RelPath: "stale.go"}}})
	a = updated.(App)
	if len(a.grep.results) != 0 {
		t.Fatalf("stale seq=1 result was applied: %+v, want no results applied", a.grep.results)
	}
	if !a.grep.searching {
		t.Error("searching should still be true — the current request (seq 2) hasn't answered yet")
	}

	// The result for the current request (seq 2) must be applied.
	updated, _ = a.Update(grepResultsMsg{seq: 2, results: []GrepResult{{RelPath: "current.go"}}})
	a = updated.(App)
	if len(a.grep.results) != 1 || a.grep.results[0].RelPath != "current.go" {
		t.Errorf("results = %+v, want the current request's result applied", a.grep.results)
	}
	if a.grep.searching {
		t.Error("searching should be false once the current request's result has landed")
	}
}

func TestGrepRegexExpr(t *testing.T) {
	cases := []struct {
		in, expr string
		ok       bool
	}{
		{`\foo`, "foo", true},
		{`\foo\`, "foo", true},
		{`hello`, "", false},
		{``, "", false},
	}
	for _, c := range cases {
		expr, ok := grepRegexExpr(c.in)
		if ok != c.ok || expr != c.expr {
			t.Errorf("grepRegexExpr(%q) = (%q,%v), want (%q,%v)", c.in, expr, ok, c.expr, c.ok)
		}
	}
}

func TestGrepResultLine(t *testing.T) {
	r := GrepResult{RelPath: "cmd/main.go", Line: 41, Col: 0, LineText: "	fmt.Println(\"hello\")"}
	line := grepResultLine(r, 80)
	if line == "" {
		t.Error("expected non-empty result line")
	}
	// Should contain the file path and 1-based line number.
	if !contains(line, "cmd/main.go") || !contains(line, "42:") {
		t.Errorf("result line missing path/number: %q", line)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsStr(s, sub))
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
