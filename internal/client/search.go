package client

import (
	"regexp"
	"unicode"
	"unicode/utf8"

	"github.com/indiejames/indigo/internal/document"
)

type searchMatch struct {
	line, col, length int
}

// regexExpr tests whether pattern is a regex search (starts with \) and
// extracts the expression to compile. The trailing \ is optional — useful
// while typing incrementally.
//
//	\foo     → expr="foo", ok=true
//	\foo\    → expr="foo", ok=true
//	foo      → "", false
func regexExpr(pattern string) (expr string, ok bool) {
	if len(pattern) == 0 || pattern[0] != '\\' {
		return "", false
	}
	expr = pattern[1:]
	if len(expr) > 0 && expr[len(expr)-1] == '\\' {
		expr = expr[:len(expr)-1]
	}
	return expr, true
}

// findMatches returns all non-overlapping matches of pattern in buf, in
// document order. Returns an error only for an invalid regex pattern.
//
// Pattern syntax:
//   - Plain text → smart-case literal search (case-insensitive unless
//     pattern contains an uppercase letter).
//   - \expr or \expr\ → Go regexp, always case-sensitive (use (?i) if needed).
func findMatches(buf *document.Buffer, pattern string) ([]searchMatch, error) {
	if pattern == "" {
		return nil, nil
	}
	if expr, ok := regexExpr(pattern); ok {
		return findRegexMatches(buf, expr)
	}
	return findLiteralMatches(buf, pattern), nil
}

// findLiteralMatches performs smart-case literal search.
func findLiteralMatches(buf *document.Buffer, pattern string) []searchMatch {
	sensitive := isSmartCaseSensitive(pattern)
	patRunes := []rune(pattern)
	if !sensitive {
		for i, r := range patRunes {
			patRunes[i] = unicode.ToLower(r)
		}
	}
	patLen := len(patRunes)

	var matches []searchMatch
	lineCount := buf.LineCount()
	for l := 0; l < lineCount; l++ {
		lineRunes := []rune(buf.Line(l))
		searchRunes := lineRunes
		if !sensitive {
			searchRunes = make([]rune, len(lineRunes))
			for i, r := range lineRunes {
				searchRunes[i] = unicode.ToLower(r)
			}
		}
		n := len(searchRunes)
		for offset := 0; offset <= n-patLen; {
			if runesMatch(searchRunes[offset:], patRunes) {
				matches = append(matches, searchMatch{line: l, col: offset, length: patLen})
				offset += patLen
			} else {
				offset++
			}
		}
	}
	return matches
}

// findRegexMatches compiles expr as a Go regexp and searches every line.
// Returns an error if expr is invalid.
func findRegexMatches(buf *document.Buffer, expr string) ([]searchMatch, error) {
	if expr == "" {
		return nil, nil
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return nil, err
	}
	var matches []searchMatch
	lineCount := buf.LineCount()
	for l := 0; l < lineCount; l++ {
		lineStr := buf.Line(l)
		for _, loc := range re.FindAllStringIndex(lineStr, -1) {
			startCol := utf8.RuneCountInString(lineStr[:loc[0]])
			endCol := utf8.RuneCountInString(lineStr[:loc[1]])
			matches = append(matches, searchMatch{
				line:   l,
				col:    startCol,
				length: endCol - startCol,
			})
		}
	}
	return matches, nil
}

// isSmartCaseSensitive returns true if pattern contains any uppercase letter.
func isSmartCaseSensitive(pattern string) bool {
	for _, r := range pattern {
		if unicode.IsUpper(r) {
			return true
		}
	}
	return false
}

func runesMatch(line, pat []rune) bool {
	for i, r := range pat {
		if i >= len(line) || line[i] != r {
			return false
		}
	}
	return true
}

// matchIdxAtOrAfter returns the index of the first match at or after (line, col),
// wrapping to 0 if none exist at or after that position. Returns -1 if empty.
func matchIdxAtOrAfter(matches []searchMatch, line, col int) int {
	if len(matches) == 0 {
		return -1
	}
	for i, m := range matches {
		if m.line > line || (m.line == line && m.col >= col) {
			return i
		}
	}
	return 0 // wrap around
}
