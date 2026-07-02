package client

import (
	"unicode"

	"github.com/indiejames/indigo/internal/document"
)

type searchMatch struct {
	line, col, length int
}

// isSmartCaseSensitive returns true if pattern contains any uppercase letter,
// triggering case-sensitive matching.
func isSmartCaseSensitive(pattern string) bool {
	for _, r := range pattern {
		if unicode.IsUpper(r) {
			return true
		}
	}
	return false
}

// findMatches returns all non-overlapping matches of pattern in buf, in
// document order. Uses smart-case: case-insensitive unless pattern has
// an uppercase letter.
func findMatches(buf *document.Buffer, pattern string) []searchMatch {
	if pattern == "" {
		return nil
	}
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
