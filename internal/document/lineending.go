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
// with crlf set, every line ends in "\r\n".
//
// A "\r\n" already present is kept as one, not doubled to "\r\r\n". The
// buffer is meant to hold "\n" only, but text inserted after loading need not
// have been normalized — a language server editing a CRLF file commonly sends
// "\r\n" in its replacement text — and a blind "\n" → "\r\n" would then
// write a stray carriage return into every such line. Collapsing first makes
// the result correct whatever reached the buffer.
func RestoreCRLF(content string, crlf bool) string {
	if !crlf {
		return content
	}
	return strings.ReplaceAll(NormalizeNewlines(content), "\n", "\r\n")
}

// NormalizeNewlines converts every "\r\n" in s to "\n", leaving any lone "\r".
//
// For text arriving from outside the buffer — a paste, an agent's replacement
// text, a language server's edit — which may carry the line endings of wherever
// it came from. Unlike NormalizeCRLF it converts whatever is there, mixed or
// not: this is new text being inserted, not a file whose existing lines a save
// must reproduce.
//
// Applied where such text *originates*, never inside Buffer.Apply: an op's text
// is also what operational transform computes positions from, and every
// replica applying different text from what the op says would diverge.
func NormalizeNewlines(s string) string {
	return strings.ReplaceAll(s, "\r\n", "\n")
}
