package client

import "github.com/indiejames/indigo/internal/config"

// detectIndentScanLimit caps how much of a buffer's content is inspected
// for indent detection, so opening a huge file doesn't pay for a full scan.
const detectIndentScanLimit = 64 * 1024

// detectIndentSettings scans a buffer's existing content for the indent
// style it already uses, so editing someone else's file stays consistent
// with it even when it differs from your own configured default. Returns
// nil when the content is empty or has no clear leading-whitespace signal.
func detectIndentSettings(content string) *config.IndentSettings {
	if len(content) > detectIndentScanLimit {
		content = content[:detectIndentScanLimit]
	}

	tabLines := 0
	spaceLines := 0
	minSpaceWidth := 0

	lineStart := 0
	for i := 0; i <= len(content); i++ {
		if i < len(content) && content[i] != '\n' {
			continue
		}
		line := content[lineStart:i]
		lineStart = i + 1

		if len(line) == 0 {
			continue
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
		return &config.IndentSettings{Style: "tabs", Width: 4}
	case minSpaceWidth > 0:
		return &config.IndentSettings{Style: "spaces", Width: minSpaceWidth}
	default:
		return nil
	}
}
