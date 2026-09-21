package workspacefs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
	results, err := Search(dir, "hello", "", "")
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
	results, err := Search(dir, "hello", "", "")
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
	results, err := Search(dir, "Hello", "", "")
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
	results, err := Search(dir, `\func [a-z]+`, "", "")
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
	_, err := Search(dir, `\[unclosed`, "", "")
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
	results, err := Search(dir, "hello", "", "")
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
	results, err := Search(dir, "TODO", "", "")
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
	results, err := Search(dir, "", "", "")
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
	results, err := Search(dir, "hello", "*.go", "")
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
	results, err := Search(dir, "hello", "", "*_test.go")
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
	results, err := Search(dir, "hello", "*.go", "*_test.go")
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
		// Recursive "**" globstar: matches zero or more path segments,
		// including directly inside the prefix and arbitrarily deep.
		{[]string{"src/**/*.ts"}, nil, "src/foo.ts", true},
		{[]string{"src/**/*.ts"}, nil, "src/a/foo.ts", true},
		{[]string{"src/**/*.ts"}, nil, "src/a/b/foo.ts", true},
		{[]string{"src/**/*.ts"}, nil, "other/foo.ts", false},
		{[]string{"**/foo.go"}, nil, "foo.go", true},
		{[]string{"**/foo.go"}, nil, "a/b/foo.go", true},
		{nil, []string{"**/vendor/**"}, "a/vendor/b/lib.go", false},
		{nil, []string{"**/vendor/**"}, "a/other/lib.go", true},
	}
	for _, c := range cases {
		got := matchGlobs(c.includes, c.excludes, c.path)
		if got != c.want {
			t.Errorf("matchGlobs(%v, %v, %q) = %v, want %v", c.includes, c.excludes, c.path, got, c.want)
		}
	}
}

// searchBuiltin is exercised directly (rather than through Search)
// so these tests cover matchGlob's globstar handling regardless of whether
// rg is on PATH — the rg backend does its own glob matching, unrelated to
// matchGlob.

func TestSearchBuiltinRecursiveGlobInclude(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"src/foo.ts":     "hello\n",
		"src/a/bar.ts":   "hello\n",
		"src/a/b/baz.ts": "hello\n",
		"src/other.go":   "hello\n",
	})
	results, err := searchBuiltin(dir, "hello", "src/**/*.ts", "", true, false)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, r := range results {
		got[r.RelPath] = true
	}
	for _, want := range []string{"src/foo.ts", "src/a/bar.ts", "src/a/b/baz.ts"} {
		if !got[want] {
			t.Errorf("expected %s in results, got %+v", want, results)
		}
	}
	if got["src/other.go"] {
		t.Errorf("src/other.go should have been excluded by include glob, got %+v", results)
	}
}

func TestSearchBuiltinRecursiveGlobExclude(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"main.go":            "hello\n",
		"vendor/lib.go":      "hello\n",
		"vendor/pkg/lib2.go": "hello\n",
	})
	results, err := searchBuiltin(dir, "hello", "", "**/vendor/**", true, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range results {
		if r.RelPath != "main.go" {
			t.Errorf("expected vendor files excluded, got %s", r.RelPath)
		}
	}
	if len(results) != 1 {
		t.Errorf("expected 1 result (main.go only), got %d: %+v", len(results), results)
	}
}

// TestGrepResultsMsgDiscardsStaleSequence is a regression test: before
// grepResultsMsg carried a sequence token, a slow older grep request's
// results arriving after a newer one's would silently overwrite the fresher
// results — e.g. searching "foo" then quickly retyping "bar" could still
// leave "foo"'s matches showing if "foo"'s search happened to take longer.

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

// TestSearchBuiltinFindsMatchesAfterVeryLongLine is a regression test: the
// built-in backend used a default bufio.Scanner, whose 64KB token limit
// made one over-long line (a minified bundle, single-line JSON, generated
// code) silently end the scan of that file — every later match dropped,
// with no error surfaced anywhere. The 5MB MaxFileBytes cap doesn't
// protect against it: a 200KB minified file passes the size check and then
// trips the line limit. An oversized line must now be skipped on its own
// while the rest of the file is still searched.

func TestSearchBuiltinFindsMatchesAfterVeryLongLine(t *testing.T) {
	longLine := strings.Repeat("x", 100_000) // well past bufio.Scanner's 64KB default
	dir := writeTree(t, map[string]string{
		"min.js": longLine + "\nvar NEEDLE = 1;\n",
	})

	results, err := searchBuiltin(dir, "NEEDLE", "", "", true, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %+v, want exactly 1 match after the long line", results)
	}
	// The skipped line must still consume a line number, so the match's
	// reported position stays correct.
	if results[0].Line != 1 {
		t.Errorf("Line = %d, want 1 (the oversized line still counts as line 0)", results[0].Line)
	}
	if results[0].RelPath != "min.js" {
		t.Errorf("RelPath = %q, want min.js", results[0].RelPath)
	}
}

// TestGrepFileSkipsOnlyTheOversizedLine verifies that a line genuinely past
// the cap is skipped on its own while normal lines on both sides of it are
// still matched — i.e. the file is not abandoned at the first over-long
// record, and the skip is surgical (and still consumes a line number, so
// later matches report correct positions). Mirrors the rg backend's
// TestSearchWithRgSkipsOversizedRecordWithoutLosingOtherMatches.
//
// Exercises grepFile directly rather than searchBuiltin: MaxFileBytes is
// both the per-line cap and the candidate-file size filter, so shrinking it
// enough to make a short line "oversized" would make searchBuiltin skip the
// whole fixture file before grepFile ever saw it.

func TestGrepFileSkipsOnlyTheOversizedLine(t *testing.T) {
	withMaxFileBytes(t, 64)                // tiny cap so a short line counts as oversized
	long := strings.Repeat("NEEDLE ", 100) // 700 bytes: past the cap above
	dir := writeTree(t, map[string]string{
		"f.txt": "NEEDLE before\n" + long + "\nNEEDLE after\n",
	})

	matchLine := func(line string) (int, int, bool) {
		i := strings.Index(line, "NEEDLE")
		if i < 0 {
			return 0, 0, false
		}
		return i, len("NEEDLE"), true
	}

	var results []Result
	grepFile("f.txt", filepath.Join(dir, "f.txt"), matchLine, &results)

	var lines []int
	for _, r := range results {
		lines = append(lines, r.Line)
	}
	if len(lines) != 2 || lines[0] != 0 || lines[1] != 2 {
		t.Errorf("matched lines = %v, want [0 2] (line 1 oversized and skipped)", lines)
	}
}
