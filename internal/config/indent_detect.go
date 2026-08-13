package config

// detectIndentScanLimit caps how much of a buffer's content is inspected
// for indent detection, so opening a huge file doesn't pay for a full scan.
const detectIndentScanLimit = 64 * 1024

// DetectIndentSettings scans content for the indent style it already uses,
// so editing or formatting an existing file stays consistent with it even
// when it differs from the configured default. Returns nil when the
// content is empty or has no clear leading-whitespace signal.
func DetectIndentSettings(content string) *IndentSettings {
	if len(content) > detectIndentScanLimit {
		content = content[:detectIndentScanLimit]
	}

	tabLines := 0
	spaceLines := 0
	minSpaceWidth := 0

	lineStart := 0
	inBlockComment := false
	for i := 0; i <= len(content); i++ {
		if i < len(content) && content[i] != '\n' {
			continue
		}
		line := content[lineStart:i]
		lineStart = i + 1

		// Strip trailing \r for CRLF line endings
		if len(line) > 0 && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}

		if len(line) == 0 {
			continue
		}

		// Remember if we started this line inside a block comment
		wasInBlockComment := inBlockComment

		// Track block comment state (/* ... */)
		for j := 0; j < len(line); j++ {
			if !inBlockComment && j+1 < len(line) && line[j] == '/' && line[j+1] == '*' {
				inBlockComment = true
				j++ // skip the '*'
			} else if inBlockComment && j+1 < len(line) && line[j] == '*' && line[j+1] == '/' {
				inBlockComment = false
				j++ // skip the '/'
			}
		}

		switch line[0] {
		case '\t':
			tabLines++
		case ' ':
			n := 0
			for n < len(line) && line[n] == ' ' {
				n++
			}
			if n == len(line) {
				continue // whitespace-only line: no real signal either way
			}
			// Block-comment continuation lines (" * foo", the closing
			// " */") conventionally align on a single space before the
			// "*" regardless of the file's real indent width — e.g. a
			// JSDoc/TSDoc comment in an otherwise 2- or 4-space-indented
			// file. Counting them would drag minSpaceWidth down to 1.
			// Only skip these when we're actually inside a block comment.
			if wasInBlockComment && line[n] == '*' && (n+1 == len(line) || line[n+1] == ' ' || line[n+1] == '/') {
				continue
			}
			spaceLines++
			if minSpaceWidth == 0 || n < minSpaceWidth {
				minSpaceWidth = n
			}
		}
	}

	switch {
	case tabLines == 0 && spaceLines == 0:
		return nil
	case tabLines > spaceLines:
		return &IndentSettings{Style: "tabs", Width: 4}
	case minSpaceWidth > 0:
		return &IndentSettings{Style: "spaces", Width: minSpaceWidth}
	default:
		return nil
	}
}
