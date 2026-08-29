package highlight

import "testing"

// TestBracketSpansLineContinuationInString is a regression test: a
// backslash-newline line continuation inside a string literal (legal in C,
// C++, and other C-style languages) used to desync BracketSpans' line
// counter for the rest of the file. The scanner's case '\\' branch inside
// stDoubleStr/stSingleStr blindly did i++ to skip the escaped character
// without checking whether that character was itself a newline — the only
// place line is incremented is the top-of-loop `if ch == '\n'` check, which
// this manual skip bypassed entirely. Every bracket colored after such a
// continuation then landed one line too early.
func TestBracketSpansLineContinuationInString(t *testing.T) {
	// Line 0: s := "abc\        (backslash-newline continues the string)
	// Line 1: def"
	// Line 2: foo(1)
	content := "s := \"abc\\\ndef\"\nfoo(1)\n"
	spans := BracketSpans([]byte(content))

	if got := spans[1]; len(got) != 0 {
		t.Errorf("spans[1] = %+v, want none (line 1 is just def\", no brackets)", got)
	}

	line2 := spans[2]
	if len(line2) != 2 {
		t.Fatalf("spans[2] = %+v, want exactly two bracket spans for foo(1)'s '(' and ')'", line2)
	}
	if line2[0].StartCol != 3 || line2[1].StartCol != 5 {
		t.Errorf("spans[2] StartCols = [%d, %d], want [3, 5] (the '(' and ')' in \"foo(1)\")", line2[0].StartCol, line2[1].StartCol)
	}
}

// TestBracketSpansLineContinuationInSingleQuotedString covers the same bug
// in the stSingleStr branch (identical shape, separate code path).
func TestBracketSpansLineContinuationInSingleQuotedString(t *testing.T) {
	content := "s := 'abc\\\ndef'\nfoo(1)\n"
	spans := BracketSpans([]byte(content))

	if got := spans[1]; len(got) != 0 {
		t.Errorf("spans[1] = %+v, want none (line 1 is just def', no brackets)", got)
	}
	line2 := spans[2]
	if len(line2) != 2 || line2[0].StartCol != 3 || line2[1].StartCol != 5 {
		t.Errorf("spans[2] = %+v, want bracket spans at StartCol 3 and 5", line2)
	}
}

// TestBracketSpansBasic is a sanity check for ordinary (non-string) bracket
// nesting, verifying depth-based color cycling and simple string-skipping
// still work as expected.
func TestBracketSpansBasic(t *testing.T) {
	content := "foo(bar(\"(not a bracket)\"))\n"
	spans := BracketSpans([]byte(content))

	line0 := spans[0]
	// Only the 4 real brackets should be colored: foo( bar( ... ) )
	// i.e. cols 3, 7, 25, 26 — the parens inside the string literal must be skipped.
	wantCols := []int{3, 7, 25, 26}
	if len(line0) != len(wantCols) {
		t.Fatalf("spans[0] = %+v, want %d spans", line0, len(wantCols))
	}
	for i, want := range wantCols {
		if line0[i].StartCol != want {
			t.Errorf("spans[0][%d].StartCol = %d, want %d", i, line0[i].StartCol, want)
		}
	}
}
