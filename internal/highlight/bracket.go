package highlight

// bracketPaletteANSI holds the cycling bracket pair colors (3 levels).
// These match VS Code's default bracket pair colorizer palette.
var bracketPaletteANSI = [3]string{
	hexToANSI("#FFD700"), // gold   — depth 0, 3, 6, …
	hexToANSI("#DA70D6"), // orchid — depth 1, 4, 7, …
	hexToANSI("#179FFF"), // blue   — depth 2, 5, 8, …
}

// BracketSpans scans content with a lightweight state machine and returns
// colored Spans for each bracket character ( ) [ ] { } that is not inside a
// string or comment. The spans use rune-based columns and are meant to be
// prepended to the syntax LineSpans so they take priority over the plain
// punctuation.bracket color.
func BracketSpans(content []byte) LineSpans {
	result := make(LineSpans)
	depth := 0

	type state uint8
	const (
		stNormal state = iota
		stLineComment  // after //
		stBlockComment // inside /* … */
		stHashComment  // after # (Python, Shell, TOML, YAML, …)
		stDoubleStr    // inside "…"
		stSingleStr    // inside '…'
		stRawStr       // inside `…` (Go raw strings, JS template literals)
	)

	src := []rune(string(content))
	n := len(src)
	line, col := 0, 0
	st := stNormal

	addSpan := func(ln, c int, color string) {
		result[ln] = append(result[ln], Span{
			StartCol: c,
			EndCol:   c + 1,
			ANSI:     color,
		})
	}

	for i := 0; i < n; i++ {
		ch := src[i]

		if ch == '\n' {
			if st == stLineComment || st == stHashComment {
				st = stNormal
			}
			line++
			col = 0
			continue
		}

		switch st {
		case stLineComment, stHashComment:
			// skip until newline (handled above)

		case stBlockComment:
			if ch == '*' && i+1 < n && src[i+1] == '/' {
				st = stNormal
				i++
				col++
			}

		case stDoubleStr:
			switch ch {
			case '\\':
				// A backslash-newline line continuation (e.g. C/C++) must
				// still advance line/col the same way the top-of-loop '\n'
				// case does — just skipping past it with i++/col++ desyncs
				// line from the scanner's real position for the rest of the
				// file, misplacing every later bracket span.
				if i+1 < n && src[i+1] == '\n' {
					i++
					line++
					col = 0
					continue // don't let the loop-bottom col++ push col to 1
				}
				i++
				col++ // skip escaped char
			case '"':
				st = stNormal
			}

		case stSingleStr:
			switch ch {
			case '\\':
				if i+1 < n && src[i+1] == '\n' {
					i++
					line++
					col = 0
					continue // don't let the loop-bottom col++ push col to 1
				}
				i++
				col++ // skip escaped char
			case '\'':
				st = stNormal
			}

		case stRawStr:
			if ch == '`' {
				st = stNormal
			}

		case stNormal:
			switch {
			case ch == '/' && i+1 < n && src[i+1] == '/':
				st = stLineComment
				i++
				col++
			case ch == '/' && i+1 < n && src[i+1] == '*':
				st = stBlockComment
				i++
				col++
			case ch == '#':
				st = stHashComment
			case ch == '"':
				st = stDoubleStr
			case ch == '\'':
				st = stSingleStr
			case ch == '`':
				st = stRawStr
			case ch == '(' || ch == '[' || ch == '{':
				addSpan(line, col, bracketPaletteANSI[depth%3])
				depth++
			case ch == ')' || ch == ']' || ch == '}':
				if depth > 0 {
					depth--
				}
				addSpan(line, col, bracketPaletteANSI[depth%3])
			}
		}
		col++
	}
	return result
}
