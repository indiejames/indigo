package client

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/indiejames/indigo/internal/document"
)

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
// document order, with no replacement text and no scope restriction — a
// convenience wrapper over findSubstituteMatches for plain search. Returns
// an error only for an invalid regex pattern.
//
// Pattern syntax:
//   - Plain text → smart-case literal search (case-insensitive unless
//     pattern contains an uppercase letter).
//   - \expr or \expr\ → Go regexp, always case-sensitive (use (?i) if needed).
func findMatches(buf *document.Buffer, pattern string) ([]substituteMatch, error) {
	return findSubstituteMatches(buf, pattern, "", nil)
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

// substituteMatch is one match found by / search (searchReplacing or not),
// paired with its replacement text. For regex patterns the replacement has
// already been expanded against this match's captured groups (Go's
// $1/$2/${name} syntax); for literal patterns there are no groups, so
// replacement is used as-is. replacement is unused (left "") for a plain
// search with no replace delimiter.
type substituteMatch struct {
	line, col, length int
	replacement       string
}

// substituteBounds restricts findSubstituteMatches to an inclusive document
// range — used to scope / search (and search-and-replace) to the active
// selection instead of the whole buffer. Both charwise and linewise
// selections store Anchor/Head such that end.Col+1 is the right exclusive
// bound, so no separate IsLine handling is needed here. A nil
// *substituteBounds passed to findSubstituteMatches means the whole buffer.
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
		if err := validateReplacementGroups(re, replacement); err != nil {
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

// validateReplacementGroups checks every $name/$number backreference in
// replacement against re's actual capture groups. Go's regexp.Expand (which
// ExpandString wraps) silently substitutes an empty string for a reference
// to a nonexistent group — e.g. $2 against a pattern with only one group —
// which would otherwise show up as silent data loss on substitute rather
// than a reported error. A malformed $ sequence (bare trailing $, no valid
// name following it) is left alone here too, matching Expand's own
// behavior of treating it as literal text.
func validateReplacementGroups(re *regexp.Regexp, replacement string) error {
	names := re.SubexpNames()
	template := replacement
	for {
		before, after, found := strings.Cut(template, "$")
		if !found {
			return nil
		}
		_ = before
		template = after
		if strings.HasPrefix(template, "$") {
			template = template[1:]
			continue
		}
		name, num, rest, ok := extractGroupName(template)
		if !ok {
			continue
		}
		template = rest
		if num >= 0 {
			if num == 0 || num > re.NumSubexp() {
				return fmt.Errorf("invalid backreference $%s: pattern has %d capture group(s)", name, re.NumSubexp())
			}
			continue
		}
		known := false
		for _, n := range names {
			if n == name {
				known = true
				break
			}
		}
		if !known {
			return fmt.Errorf("invalid backreference $%s: no such named group in pattern", name)
		}
	}
}

// extractGroupName parses a $name/${name} reference at the start of str
// (str already past the leading '$'), mirroring the unexported parsing
// regexp.Regexp.Expand itself uses. num is >= 0 for a purely numeric name
// (the submatch index), -1 for a named group. ok is false for a malformed
// reference (empty name, or an unclosed brace), which Expand treats as
// literal text rather than a reference.
func extractGroupName(str string) (name string, num int, rest string, ok bool) {
	brace := strings.HasPrefix(str, "{")
	if brace {
		str = str[1:]
	}
	i := 0
	for i < len(str) {
		r, size := utf8.DecodeRuneInString(str[i:])
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' {
			break
		}
		i += size
	}
	if i == 0 {
		return "", 0, "", false
	}
	name = str[:i]
	if brace {
		if i >= len(str) || str[i] != '}' {
			return "", 0, "", false
		}
		i++
	}
	num = 0
	for j := 0; j < len(name); j++ {
		if name[j] < '0' || name[j] > '9' {
			num = -1
			break
		}
		num = num*10 + int(name[j]-'0')
	}
	if num >= 0 && name[0] == '0' && len(name) > 1 {
		num = -1 // leading zero: not a valid submatch index, treat as a named group
	}
	return name, num, str[i:], true
}

// matchIdxAtOrAfter returns the index of the first match at or after (line, col),
// wrapping to 0 if none exist at or after that position. Returns -1 if empty.
func matchIdxAtOrAfter(matches []substituteMatch, line, col int) int {
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

// splitSearchQuery parses the raw text typed after '/' into a search
// pattern and, when an unescaped '/' delimiter is present, a replacement —
// switching search into live search-and-replace preview. A literal '/'
// inside the pattern is written '\/'; once past the delimiter, everything
// else typed is the replacement verbatim (there's no third field, unlike
// vim's :s flags — a second or later unescaped '/' is just replacement
// text).
func splitSearchQuery(query string) (pattern, replacement string, isReplace bool) {
	parts := splitUnescaped(query, '/')
	if len(parts) < 2 {
		return parts[0], "", false
	}
	return parts[0], strings.Join(parts[1:], "/"), true
}

// splitUnescaped splits s on sep, treating "\"+sep as a literal, unescaped
// sep character rather than a delimiter, and "\\" as a literal backslash —
// so a run of backslashes immediately before sep resolves the same way
// standard escaping does (an escaped backslash can't also escape the
// separator behind it). Other backslash sequences (e.g. a regex pattern's
// \d) are left untouched.
func splitUnescaped(s string, sep rune) []string {
	var parts []string
	var cur strings.Builder
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if r == '\\' && i+1 < len(runes) && (runes[i+1] == sep || runes[i+1] == '\\') {
			cur.WriteRune(runes[i+1])
			i++
			continue
		}
		if r == sep {
			parts = append(parts, cur.String())
			cur.Reset()
			continue
		}
		cur.WriteRune(r)
	}
	parts = append(parts, cur.String())
	return parts
}
