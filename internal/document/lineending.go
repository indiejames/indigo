package document

import "strings"

// NormalizeCRLF converts content whose lines all end in "\r\n" to "\n" line
// endings, and reports whether it did.
//
// A buffer holds "\n" line endings only. A "\r" left in the text is an ordinary
// character to everything downstream: the cursor can rest on it at the end of
// every line, searches and edits see it, and a terminal told to draw it moves
// the cursor back to column 0 and overwrites the line — which is what a
// Windows-edited file looked like.
//
// Only a file that is CRLF throughout is converted. A file with mixed endings is
// returned unchanged: SaveCRLF would otherwise rewrite every LF-only line on the
// next save, turning a one-character edit into a whole-file diff. Its stray
// "\r"s are left for the renderer to draw visibly.
func NormalizeCRLF(content string) (string, bool) {
	crlf := strings.Count(content, "\r\n")
	if crlf == 0 || crlf != strings.Count(content, "\n") {
		return content, false
	}
	return strings.ReplaceAll(content, "\r\n", "\n"), true
}

// RestoreCRLF is NormalizeCRLF's inverse for writing a buffer back to disk:
// with crlf set, every "\n" becomes "\r\n". The buffer itself never holds a
// "\r\n" when crlf is set, so this cannot double one up.
func RestoreCRLF(content string, crlf bool) string {
	if !crlf {
		return content
	}
	return strings.ReplaceAll(content, "\n", "\r\n")
}
