package client

import (
	"strconv"
	"strings"
)

// snippetStop is one argument-placeholder tab stop on the snippet line, as
// [start, end) exclusive columns. An empty range (start == end) is a bare cursor
// position — used for the final stop after the closing paren.
type snippetStop struct{ start, end int }

// parseSignatureParams extracts the parameter names from a resolved completion
// detail's call signature. For example
//
//	"(alias) function greet(name: string, times: number): void"
//
// yields ["name", "times"]. Every line of detail is checked, in order, for the
// first one containing a parenthesized parameter list: an already-imported
// function's signature is on the first line, but a not-yet-imported auto-import
// candidate's detail leads with a preamble line instead — e.g.
// "Auto import from './helper'\nfunction bar(x: number, y: number): number" —
// so the signature only appears on the second line there. Returns nil when no
// call signature is found on any line (the caller then inserts a bare "()").
// Used to prefill argument
// placeholders when accepting a callable completion, since imported functions
// arrive as kind Variable (an alias), so server-side function-call snippets
// aren't produced for them.
func parseSignatureParams(detail string) []string {
	for _, line := range strings.Split(detail, "\n") {
		sig := stripSignatureTag(line)
		open := strings.IndexByte(sig, '(')
		if open < 0 {
			continue // e.g. the "Auto import from '...'" preamble line
		}
		segs, ok := extractParamSegments(sig[open:])
		if !ok {
			continue
		}
		var names []string
		for i, seg := range segs {
			name := paramName(seg)
			if name == "" {
				if strings.TrimSpace(seg) == "" {
					continue // empty param list (no-arg call)
				}
				name = "arg" + strconv.Itoa(i+1) // destructured/unnameable param
			}
			names = append(names, name)
		}
		return names
	}
	return nil
}

// stripSignatureTag removes a leading tsserver kind tag like "(alias) ",
// "(method) ", or "(property) " so the real signature that follows can be
// parsed. A tag has no ':' inside its parens; a parameter list always does
// (params are typed, e.g. "name: string"), so a leading "(...)" containing ':'
// is left in place.
func stripSignatureTag(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "(") {
		return s
	}
	close := strings.IndexByte(s, ')')
	if close < 0 || strings.ContainsRune(s[:close], ':') {
		return s
	}
	return strings.TrimSpace(s[close+1:])
}

// extractParamSegments returns the top-level, comma-separated parameter segments
// between the leading '(' of s and its matching ')'. It tracks () [] {} <>
// nesting so commas inside generics/object types/nested calls don't split a
// param, and treats "=>" as an arrow rather than a closing angle bracket. ok is
// false if s doesn't start with '(' or the parentheses are unbalanced.
func extractParamSegments(s string) (segs []string, ok bool) {
	rs := []rune(s)
	if len(rs) == 0 || rs[0] != '(' {
		return nil, false
	}
	var round, square, curly, angle int
	start := 1
	for i := 0; i < len(rs); i++ {
		switch rs[i] {
		case '(':
			round++
		case ')':
			round--
			if round == 0 && square == 0 && curly == 0 && angle == 0 {
				return append(segs, string(rs[start:i])), true
			}
		case '[':
			square++
		case ']':
			square--
		case '{':
			curly++
		case '}':
			curly--
		case '<':
			angle++
		case '>':
			if i > 0 && rs[i-1] == '=' {
				// "=>" arrow, not a generic close.
			} else if angle > 0 {
				angle--
			}
		case ',':
			if round == 1 && square == 0 && curly == 0 && angle == 0 {
				segs = append(segs, string(rs[start:i]))
				start = i + 1
			}
		}
	}
	return nil, false
}

// paramName returns the leading identifier of a parameter segment, e.g.
// "name: string" -> "name", "times?: number" -> "times", "...rest: T[]" ->
// "rest". Returns "" for a destructured or otherwise unnameable param.
func paramName(seg string) string {
	seg = strings.TrimSpace(seg)
	seg = strings.TrimSpace(strings.TrimPrefix(seg, "..."))
	end := 0
	for end < len(seg) {
		r := seg[end]
		if r == '_' || r == '$' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			end++
		} else {
			break
		}
	}
	return seg[:end]
}
