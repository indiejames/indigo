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

// substituteMatch is one match found for :s, paired with its replacement
// text. For regex patterns the replacement has already been expanded
// against this match's captured groups (Go's $1/$2/${name} syntax); for
// literal patterns there are no groups, so replacement is used as-is.
type substituteMatch struct {
	line, col, length int
	replacement       string
}

// substituteBounds restricts findSubstituteMatches to an inclusive document
// range — used to scope :s to the active selection (charwise or linewise;
// both store Anchor/Head such that end.Col+1 is the right exclusive bound,
// so no separate IsLine handling is needed here). A nil *substituteBounds
// passed to findSubstituteMatches means the whole buffer.
type substituteBounds struct {
	from, to document.Pos
}

// findSubstituteMatches finds every match of pattern in buf and pairs each
// with its replacement text, in document order (top to bottom, left to
// right — the same order findMatches uses). bounds, if non-nil, restricts
// matches to that inclusive range.
//
// Pattern syntax matches findMatches: \expr triggers Go regexp, and
// replacement may reference captured groups with $1, $2, ${name}, etc.
// (expanded per match via regexp.Regexp.ExpandString); plain text is
// smart-case literal search, and replacement is used verbatim since there
// are no groups to expand.
func findSubstituteMatches(buf *document.Buffer, pattern, replacement string, bounds *substituteBounds) ([]substituteMatch, error) {
	if pattern == "" {
		return nil, nil
	}
	startLine, endLine := 0, buf.LineCount()-1
	if bounds != nil {
		startLine, endLine = bounds.from.Line, bounds.to.Line
	}
	// colBounds returns the [lo, hi) column window to search within on line
	// l, given lineLen runes on that line — the full line except on the
	// first/last line of a bounded range, mirroring textForSelection.
	colBounds := func(l, lineLen int) (lo, hi int) {
		lo, hi = 0, lineLen
		if bounds == nil {
			return
		}
		if l == bounds.from.Line {
			lo = bounds.from.Col
		}
		if l == bounds.to.Line {
			hi = min(hi, bounds.to.Col+1)
		}
		return
	}

	if expr, ok := regexExpr(pattern); ok {
		if expr == "" {
			return nil, nil
		}
		re, err := regexp.Compile(expr)
		if err != nil {
			return nil, err
		}
		var out []substituteMatch
		for l := startLine; l <= endLine; l++ {
			lineStr := buf.Line(l)
			lo, hi := colBounds(l, utf8.RuneCountInString(lineStr))
			for _, sub := range re.FindAllStringSubmatchIndex(lineStr, -1) {
				startCol := utf8.RuneCountInString(lineStr[:sub[0]])
				endCol := utf8.RuneCountInString(lineStr[:sub[1]])
				if startCol < lo || endCol > hi {
					continue
				}
				rep := re.ExpandString(nil, replacement, lineStr, sub)
				out = append(out, substituteMatch{line: l, col: startCol, length: endCol - startCol, replacement: string(rep)})
			}
		}
		return out, nil
	}

	sensitive := isSmartCaseSensitive(pattern)
	patRunes := []rune(pattern)
	if !sensitive {
		for i, r := range patRunes {
			patRunes[i] = unicode.ToLower(r)
		}
	}
	patLen := len(patRunes)
	var out []substituteMatch
	for l := startLine; l <= endLine; l++ {
		lineRunes := []rune(buf.Line(l))
		lo, hi := colBounds(l, len(lineRunes))
		searchRunes := lineRunes
		if !sensitive {
			searchRunes = make([]rune, len(lineRunes))
			for i, r := range lineRunes {
				searchRunes[i] = unicode.ToLower(r)
			}
		}
		for offset := lo; offset+patLen <= hi; {
			if runesMatch(searchRunes[offset:], patRunes) {
				out = append(out, substituteMatch{line: l, col: offset, length: patLen, replacement: replacement})
				offset += patLen
			} else {
				offset++
			}
		}
	}
	return out, nil
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
