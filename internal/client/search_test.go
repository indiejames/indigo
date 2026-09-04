package client

import (
	"testing"

	"github.com/indiejames/indigo/internal/document"
)

func TestIsSmartCaseSensitive(t *testing.T) {
	cases := []struct {
		pat  string
		want bool
	}{
		{"hello", false},
		{"Hello", true},
		{"WORLD", true},
		{"", false},
		{"foo123", false},
		{"Foo123", true},
	}
	for _, c := range cases {
		if got := isSmartCaseSensitive(c.pat); got != c.want {
			t.Errorf("isSmartCaseSensitive(%q) = %v, want %v", c.pat, got, c.want)
		}
	}
}

func TestFindMatchesBasic(t *testing.T) {
	buf := document.New("", "hello world\nhello again\n")
	matches, err := findMatches(buf, "hello")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 2 {
		t.Fatalf("expected 2 matches, got %d", len(matches))
	}
	if matches[0].line != 0 || matches[0].col != 0 {
		t.Errorf("match[0]: got line=%d col=%d, want 0,0", matches[0].line, matches[0].col)
	}
	if matches[1].line != 1 || matches[1].col != 0 {
		t.Errorf("match[1]: got line=%d col=%d, want 1,0", matches[1].line, matches[1].col)
	}
}

func TestFindMatchesCaseInsensitive(t *testing.T) {
	buf := document.New("", "Hello World\n")
	matches, err := findMatches(buf, "hello")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("case-insensitive: expected 1 match, got %d", len(matches))
	}
}

func TestFindMatchesCaseSensitive(t *testing.T) {
	buf := document.New("", "Hello hello\n")
	matches, err := findMatches(buf, "Hello")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("case-sensitive: expected 1 match, got %d", len(matches))
	}
	if matches[0].col != 0 {
		t.Errorf("case-sensitive: expected col 0, got %d", matches[0].col)
	}
}

func TestFindMatchesEmpty(t *testing.T) {
	buf := document.New("", "hello\n")
	matches, err := findMatches(buf, "")
	if err != nil || matches != nil {
		t.Errorf("empty pattern: expected nil,nil, got %v,%v", matches, err)
	}
}

func TestFindMatchesNone(t *testing.T) {
	buf := document.New("", "hello\n")
	matches, err := findMatches(buf, "xyz")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Errorf("no matches: expected 0, got %d", len(matches))
	}
}

// --- regex search ---

func TestRegexExpr(t *testing.T) {
	cases := []struct {
		input    string
		wantExpr string
		wantOK   bool
	}{
		{`\d+`, "d+", true},
		{`\d+\`, "d+", true},
		{`\foo.*bar\`, "foo.*bar", true},
		{`hello`, "", false},
		{``, "", false},
		{`\`, "", true},
		{`\\`, "", true},
	}
	for _, c := range cases {
		expr, ok := regexExpr(c.input)
		if ok != c.wantOK || expr != c.wantExpr {
			t.Errorf("regexExpr(%q) = (%q, %v), want (%q, %v)", c.input, expr, ok, c.wantExpr, c.wantOK)
		}
	}
}

func TestFindMatchesRegexBasic(t *testing.T) {
	// \[0-9]+ → leading \ consumed as regex marker, expr=[0-9]+ → digit sequences.
	buf := document.New("", "foo123 bar456\n")
	matches, err := findMatches(buf, `\[0-9]+`)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 2 {
		t.Fatalf("regex [0-9]+: expected 2 matches, got %d", len(matches))
	}
	if matches[0].col != 3 || matches[0].length != 3 {
		t.Errorf("match[0]: col=%d length=%d, want col=3 length=3", matches[0].col, matches[0].length)
	}
	if matches[1].col != 10 || matches[1].length != 3 {
		t.Errorf("match[1]: col=%d length=%d, want col=10 length=3", matches[1].col, matches[1].length)
	}
}

func TestFindMatchesRegexCaseSensitive(t *testing.T) {
	buf := document.New("", "Hello hello HELLO\n")
	// Regex is always case-sensitive.
	matches, err := findMatches(buf, `\hello`)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("regex case-sensitive: expected 1 match, got %d", len(matches))
	}
	if matches[0].col != 6 {
		t.Errorf("regex case-sensitive: col=%d, want 6", matches[0].col)
	}
}

func TestFindMatchesRegexCaseInsensitiveFlag(t *testing.T) {
	buf := document.New("", "Hello HELLO hello\n")
	matches, err := findMatches(buf, `\(?i)hello`)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 3 {
		t.Fatalf("regex (?i): expected 3 matches, got %d", len(matches))
	}
}

func TestFindMatchesRegexWithClosingDelimiter(t *testing.T) {
	buf := document.New("", "abc123def\n")
	matches, err := findMatches(buf, `\[a-z]+\`)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 2 {
		t.Fatalf("regex with closing \\: expected 2, got %d", len(matches))
	}
}

func TestFindMatchesRegexInvalid(t *testing.T) {
	buf := document.New("", "hello\n")
	_, err := findMatches(buf, `\[unclosed`)
	if err == nil {
		t.Error("invalid regex: expected error, got nil")
	}
}

func TestFindMatchesRegexMultipleLines(t *testing.T) {
	buf := document.New("", "func foo() {\n}\nfunc bar() {\n}\n")
	matches, err := findMatches(buf, `\func \w+`)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 2 {
		t.Fatalf("regex multi-line: expected 2, got %d", len(matches))
	}
	if matches[0].line != 0 || matches[1].line != 2 {
		t.Errorf("regex multi-line: lines %d,%d, want 0,2", matches[0].line, matches[1].line)
	}
}

// --- substitute matching ---

func TestFindSubstituteMatchesLiteral(t *testing.T) {
	buf := document.New("", "foo bar foo\n")
	matches, err := findSubstituteMatches(buf, "foo", "baz", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 2 {
		t.Fatalf("expected 2 matches, got %d", len(matches))
	}
	for _, m := range matches {
		if m.replacement != "baz" {
			t.Errorf("replacement = %q, want baz", m.replacement)
		}
	}
}

func TestFindSubstituteMatchesRegexBackreferences(t *testing.T) {
	buf := document.New("", "alice-bob carol-dave\n")
	matches, err := findSubstituteMatches(buf, `\(\w+)-(\w+)`, "$2-$1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 2 {
		t.Fatalf("expected 2 matches, got %d", len(matches))
	}
	if matches[0].replacement != "bob-alice" {
		t.Errorf("match[0].replacement = %q, want bob-alice", matches[0].replacement)
	}
	if matches[1].replacement != "dave-carol" {
		t.Errorf("match[1].replacement = %q, want dave-carol", matches[1].replacement)
	}
}

func TestFindSubstituteMatchesLiteralHasNoBackreferences(t *testing.T) {
	// $1 has no special meaning outside regex mode — used verbatim.
	buf := document.New("", "a$1b\n")
	matches, err := findSubstituteMatches(buf, "a$1b", "$1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected 1 match, got %d", len(matches))
	}
	if matches[0].replacement != "$1" {
		t.Errorf("replacement = %q, want $1", matches[0].replacement)
	}
}

func TestFindSubstituteMatchesBoundedToOneLine(t *testing.T) {
	buf := document.New("", "foo\nfoo\nfoo\n")
	bounds := &substituteBounds{from: document.Pos{Line: 1, Col: 0}, to: document.Pos{Line: 1, Col: 2}}
	matches, err := findSubstituteMatches(buf, "foo", "bar", bounds)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].line != 1 {
		t.Fatalf("expected 1 match on line 1, got %+v", matches)
	}
}

func TestFindSubstituteMatchesBoundedPartialLine(t *testing.T) {
	// Bounds cover only "foo foo" — up to but not including the third foo.
	buf := document.New("", "foo foo foo\n")
	bounds := &substituteBounds{from: document.Pos{Line: 0, Col: 0}, to: document.Pos{Line: 0, Col: 6}}
	matches, err := findSubstituteMatches(buf, "foo", "x", bounds)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 2 {
		t.Fatalf("expected 2 matches within bounds, got %d", len(matches))
	}
}

func TestFindSubstituteMatchesInvalidRegex(t *testing.T) {
	buf := document.New("", "hello\n")
	_, err := findSubstituteMatches(buf, `\[unclosed`, "x", nil)
	if err == nil {
		t.Error("invalid regex: expected error, got nil")
	}
}

func TestFindSubstituteMatchesInvalidBackreferenceOutOfRange(t *testing.T) {
	// Pattern has only 1 capture group; $2 is out of range and would
	// silently expand to "" via regexp.Expand — must surface as an error
	// instead of quietly discarding matched text.
	buf := document.New("", "alice-bob\n")
	_, err := findSubstituteMatches(buf, `\(\w+)-\w+`, "$1-$2", nil)
	if err == nil {
		t.Error("out-of-range backreference: expected error, got nil")
	}
}

func TestFindSubstituteMatchesInvalidBackreferenceZero(t *testing.T) {
	buf := document.New("", "alice-bob\n")
	_, err := findSubstituteMatches(buf, `\(\w+)-(\w+)`, "$0", nil)
	if err == nil {
		t.Error("$0 backreference: expected error, got nil")
	}
}

func TestFindSubstituteMatchesInvalidNamedBackreference(t *testing.T) {
	buf := document.New("", "alice-bob\n")
	_, err := findSubstituteMatches(buf, `\(?P<first>\w+)-(\w+)`, "${missing}", nil)
	if err == nil {
		t.Error("unknown named backreference: expected error, got nil")
	}
}

func TestFindSubstituteMatchesValidNamedBackreference(t *testing.T) {
	buf := document.New("", "alice-bob\n")
	matches, err := findSubstituteMatches(buf, `\(?P<first>\w+)-(?P<second>\w+)`, "${second}-${first}", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].replacement != "bob-alice" {
		t.Fatalf("expected 1 match with replacement bob-alice, got %+v", matches)
	}
}

func TestFindSubstituteMatchesDollarDollarIsLiteral(t *testing.T) {
	// $$ is a literal '$', not a backreference — must not be flagged even
	// though the digit(s) following it (here, out-of-range $9) would be
	// invalid as a real reference.
	buf := document.New("", "foo\n")
	matches, err := findSubstituteMatches(buf, `\foo`, "$$9", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].replacement != "$9" {
		t.Fatalf("expected literal replacement $9, got %+v", matches)
	}
}

func TestMatchIdxAtOrAfter(t *testing.T) {
	matches := []substituteMatch{
		{line: 0, col: 5},
		{line: 2, col: 0},
		{line: 4, col: 3},
	}
	// Exact match
	if i := matchIdxAtOrAfter(matches, 2, 0); i != 1 {
		t.Errorf("exact: got %d, want 1", i)
	}
	// After end → wraps to 0
	if i := matchIdxAtOrAfter(matches, 10, 0); i != 0 {
		t.Errorf("wrap: got %d, want 0", i)
	}
	// Before first
	if i := matchIdxAtOrAfter(matches, 0, 0); i != 0 {
		t.Errorf("before first: got %d, want 0", i)
	}
}

func TestMatchIdxAtOrAfterEmpty(t *testing.T) {
	if i := matchIdxAtOrAfter(nil, 0, 0); i != -1 {
		t.Errorf("empty: got %d, want -1", i)
	}
}

func TestSplitSearchQuery(t *testing.T) {
	cases := []struct {
		query       string
		wantPattern string
		wantRepl    string
		wantReplace bool
	}{
		{"foo", "foo", "", false},
		{"", "", "", false},
		{"foo/bar", "foo", "bar", true},
		{"foo/", "foo", "", true}, // empty replacement = delete matches
		{`foo\/bar`, "foo/bar", "", false},
		{`foo\/bar/baz`, "foo/bar", "baz", true},
		{`\d+/N`, `\d+`, "N", true},             // regex-trigger backslash untouched
		{"foo/bar/baz", "foo", "bar/baz", true}, // no third field — rest is replacement
	}
	for _, c := range cases {
		pattern, repl, isReplace := splitSearchQuery(c.query)
		if pattern != c.wantPattern || repl != c.wantRepl || isReplace != c.wantReplace {
			t.Errorf("splitSearchQuery(%q) = (%q,%q,%v), want (%q,%q,%v)",
				c.query, pattern, repl, isReplace, c.wantPattern, c.wantRepl, c.wantReplace)
		}
	}
}

// --- integration: entering search mode and navigating ---

func TestSearchModeEnterAndCancel(t *testing.T) {
	m := newTestModel("hello world\nhello again\n")
	m.cursor.Line = 0
	m.cursor.Col = 0

	// Press "/" to enter search mode.
	m2, _ := m.handleKey(fakeKey("/"))
	got := m2.(Model)
	if got.mode != ModeSearch {
		t.Fatalf("after '/': mode = %v, want ModeSearch", got.mode)
	}

	// Type "hello".
	for _, ch := range "hello" {
		m2, _ = got.handleKey(fakeKey(string(ch)))
		got = m2.(Model)
	}
	if got.searchQuery != "hello" {
		t.Errorf("searchQuery = %q, want 'hello'", got.searchQuery)
	}
	if len(got.searchMatches) != 2 {
		t.Errorf("searchMatches = %d, want 2", len(got.searchMatches))
	}

	// Esc cancels and restores cursor.
	origin := got.searchOrigin
	m2, _ = got.handleKey(fakeKey("esc"))
	got = m2.(Model)
	if got.mode != ModeNormal {
		t.Errorf("after esc: mode = %v, want ModeNormal", got.mode)
	}
	if got.cursor != origin {
		t.Errorf("cursor not restored: got %v, want %v", got.cursor, origin)
	}
	if got.searchQuery != "" {
		t.Errorf("searchQuery not cleared after esc")
	}
}

func TestSearchModeEnterConfirm(t *testing.T) {
	m := newTestModel("hello world\nhello again\n")

	m2, _ := m.handleKey(fakeKey("/"))
	got := m2.(Model)
	for _, ch := range "hello" {
		m2, _ = got.handleKey(fakeKey(string(ch)))
		got = m2.(Model)
	}
	// Enter confirms.
	m2, _ = got.handleKey(fakeKey("enter"))
	got = m2.(Model)
	if got.mode != ModeNormal {
		t.Errorf("after enter: mode = %v, want ModeNormal", got.mode)
	}
	// Cursor should be on a match.
	if got.cursor.Line != 0 || got.cursor.Col != 0 {
		t.Errorf("cursor after confirm: got %v, want {0,0}", got.cursor)
	}
}

func TestSearchNavigation(t *testing.T) {
	m := newTestModel("aaa bbb\naaa ccc\n")
	// Prime matches by going through search mode.
	m2, _ := m.handleKey(fakeKey("/"))
	got := m2.(Model)
	for _, ch := range "aaa" {
		m2, _ = got.handleKey(fakeKey(string(ch)))
		got = m2.(Model)
	}
	m2, _ = got.handleKey(fakeKey("enter"))
	got = m2.(Model)
	// Should be on first match.
	if got.cursor.Line != 0 || got.cursor.Col != 0 {
		t.Errorf("first match: got %v, want {0,0}", got.cursor)
	}

	// n → next match.
	m2, _ = got.handleKey(fakeKey("n"))
	got = m2.(Model)
	if got.cursor.Line != 1 || got.cursor.Col != 0 {
		t.Errorf("n: got %v, want {1,0}", got.cursor)
	}

	// N → previous match.
	m2, _ = got.handleKey(fakeKey("N"))
	got = m2.(Model)
	if got.cursor.Line != 0 || got.cursor.Col != 0 {
		t.Errorf("N: got %v, want {0,0}", got.cursor)
	}
}

func TestExtractGroupNameLargeNumericName(t *testing.T) {
	// Regression test: an all-digit name with 10+ digits should be treated
	// as a named group (num == -1) rather than overflowing or being parsed
	// as a huge numeric submatch index.
	cases := []struct {
		input       string
		wantName    string
		wantNum     int
		wantRest    string
		wantOK      bool
		description string
	}{
		// 10-digit all-numeric name: should be treated as named group
		{"1234567890", "1234567890", -1, "", true, "10-digit numeric name"},
		// 15-digit all-numeric name: should be treated as named group
		{"123456789012345", "123456789012345", -1, "", true, "15-digit numeric name"},
		// Normal small numeric reference: should parse as submatch index
		{"1", "1", 1, "", true, "single digit"},
		{"12", "12", 12, "", true, "two digits"},
		{"123", "123", 123, "", true, "three digits"},
		// Braced 10-digit numeric name: should be treated as named group
		{"{1234567890}", "1234567890", -1, "", true, "braced 10-digit numeric name"},
		// Leading zero is already handled as named group
		{"01", "01", -1, "", true, "leading zero"},
	}

	for _, c := range cases {
		name, num, rest, ok := extractGroupName(c.input)
		if name != c.wantName || num != c.wantNum || rest != c.wantRest || ok != c.wantOK {
			t.Errorf("%s: extractGroupName(%q) = (%q, %d, %q, %v), want (%q, %d, %q, %v)",
				c.description, c.input, name, num, rest, ok, c.wantName, c.wantNum, c.wantRest, c.wantOK)
		}
	}
}

// TestWithClearedSearchRemembersLastQuery is a regression test for the
// user-requested "semi-persistent search" feature: clearing search state
// (Esc in Normal mode, or any of withClearedSearch's other callers) must
// preserve the pattern in lastSearchQuery rather than discarding it, so
// n/N can revive it later (see reactivateLastSearch).
func TestWithClearedSearchRemembersLastQuery(t *testing.T) {
	m := newTestModel("hello world\n")
	m.searchQuery = "hello"

	got := m.withClearedSearch()

	if got.searchQuery != "" {
		t.Errorf("searchQuery = %q, want empty after clearing", got.searchQuery)
	}
	if got.lastSearchQuery != "hello" {
		t.Errorf("lastSearchQuery = %q, want %q", got.lastSearchQuery, "hello")
	}
}

// TestWithClearedSearchKeepsPriorLastQueryWhenAlreadyEmpty verifies a
// second, redundant clear (searchQuery already "") doesn't stomp a
// previously remembered lastSearchQuery back to empty.
func TestWithClearedSearchKeepsPriorLastQueryWhenAlreadyEmpty(t *testing.T) {
	m := newTestModel("hello world\n")
	m.lastSearchQuery = "hello"
	m.searchQuery = ""

	got := m.withClearedSearch()

	if got.lastSearchQuery != "hello" {
		t.Errorf("lastSearchQuery = %q, want unchanged %q", got.lastSearchQuery, "hello")
	}
}

// TestReactivateLastSearchNoQuery verifies a no-op (ok=false, m unchanged)
// when nothing has ever been searched for.
func TestReactivateLastSearchNoQuery(t *testing.T) {
	m := newTestModel("hello world\n")

	got, ok := m.reactivateLastSearch()
	if ok {
		t.Fatal("expected ok=false with no lastSearchQuery")
	}
	if len(got.searchMatches) != 0 {
		t.Errorf("searchMatches = %v, want empty", got.searchMatches)
	}
}

// TestReactivateLastSearchNoMatches verifies a remembered query that no
// longer matches anything (e.g. the buffer changed since) reports an error
// status instead of jumping anywhere.
func TestReactivateLastSearchNoMatches(t *testing.T) {
	m := newTestModel("goodbye world\n")
	m.lastSearchQuery = "hello"

	got, ok := m.reactivateLastSearch()
	if ok {
		t.Fatal("expected ok=false when the pattern no longer matches")
	}
	if !isErrMessage(got.status) {
		t.Errorf("status = %q, want an error status", got.status)
	}
}

// TestReactivateLastSearchJumpsToFirstMatch verifies the happy path: a
// remembered query that still matches populates searchMatches fresh and
// moves the cursor to the first match, regardless of where the cursor
// currently sits.
func TestReactivateLastSearchJumpsToFirstMatch(t *testing.T) {
	m := newTestModel("hello world\nhello again\nhello once more\n")
	m.lastSearchQuery = "hello"
	m.cursor = document.Pos{Line: 2, Col: 5} // far from the first match

	got, ok := m.reactivateLastSearch()
	if !ok {
		t.Fatal("expected ok=true")
	}
	if len(got.searchMatches) != 3 {
		t.Fatalf("searchMatches = %d, want 3", len(got.searchMatches))
	}
	if got.searchIdx != 0 {
		t.Errorf("searchIdx = %d, want 0", got.searchIdx)
	}
	if got.cursor.Line != 0 || got.cursor.Col != 0 {
		t.Errorf("cursor = %+v, want {0 0} (first match)", got.cursor)
	}
}

// TestNAfterEscReactivatesLastSearch is the full end-to-end scenario from
// the user's request: search, use n to jump around, Esc to clear
// (highlighting/state gone), then later pressing n again should revive the
// same search and land on its first match rather than doing nothing.
func TestNAfterEscReactivatesLastSearch(t *testing.T) {
	m := newTestModel("hello world\nhello again\nhello once more\n")
	got := typeInSearch(t, m, "hello")

	m2, _ := got.handleKey(fakeKey("enter")) // commit the plain search
	got = m2.(Model)
	if len(got.searchMatches) == 0 {
		t.Fatal("expected matches after committing the search")
	}

	m2, _ = got.handleNormal(fakeKey("n")) // jump around a bit, as described
	got = m2.(Model)

	m2, _ = got.handleNormal(fakeKey("esc")) // "break out"
	got = m2.(Model)
	if len(got.searchMatches) != 0 {
		t.Fatal("expected search state cleared after Esc")
	}
	if got.lastSearchQuery != "hello" {
		t.Fatalf("lastSearchQuery = %q, want %q", got.lastSearchQuery, "hello")
	}

	// "At some later date" — move the cursor away first, to prove this
	// isn't just relying on the cursor already sitting on a match.
	got.cursor = document.Pos{Line: 2, Col: 5}
	m2, _ = got.handleNormal(fakeKey("n"))
	got = m2.(Model)

	if got.cursor.Line != 0 || got.cursor.Col != 0 {
		t.Errorf("cursor after reviving n = %+v, want {0 0} (first match)", got.cursor)
	}
	if len(got.searchMatches) != 3 {
		t.Errorf("searchMatches after reviving = %d, want 3", len(got.searchMatches))
	}
}
