package document

import (
	"strings"
	"sync"
	"unicode/utf8"
)

// Pos is a line/column position (both 0-based).
type Pos struct {
	Line, Col int
}

// OpType distinguishes edit operations.
type OpType int

const (
	OpNoop   OpType = 0
	OpInsert OpType = 1
	OpDelete OpType = 2
)

// Op is a single atomic edit to the buffer.
type Op struct {
	ClientID uint64
	Version  uint64
	Type     OpType

	// Insert
	InsertLine int
	InsertCol  int
	InsertText string

	// Delete
	FromLine, FromCol int
	ToLine, ToCol     int
}

// Buffer holds the text of a single file.
// All public methods are safe for concurrent use.
type Buffer struct {
	mu       sync.RWMutex
	lines    [][]rune
	version  uint64
	history  []Op // ops since open, indexed by version-1
	path     string
	dirty    bool
}

// New creates a Buffer from raw file content.
func New(path, content string) *Buffer {
	raw := strings.Split(content, "\n")
	lines := make([][]rune, len(raw))
	for i, l := range raw {
		lines[i] = []rune(l)
	}
	return &Buffer{path: path, lines: lines}
}

func (b *Buffer) Path() string { return b.path }
func (b *Buffer) Dirty() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.dirty
}

// Version returns the current version counter.
func (b *Buffer) Version() uint64 {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.version
}

// Content returns the full text as a single string.
func (b *Buffer) Content() string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.content()
}

func (b *Buffer) content() string {
	parts := make([]string, len(b.lines))
	for i, l := range b.lines {
		parts[i] = string(l)
	}
	return strings.Join(parts, "\n")
}

// LineCount returns the number of lines.
func (b *Buffer) LineCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.lines)
}

// Line returns the text of line n (0-based). Returns "" if out of range.
func (b *Buffer) Line(n int) string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if n < 0 || n >= len(b.lines) {
		return ""
	}
	return string(b.lines[n])
}

// LineLen returns the rune length of line n.
func (b *Buffer) LineLen(n int) int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if n < 0 || n >= len(b.lines) {
		return 0
	}
	return len(b.lines[n])
}

// Apply applies op to the buffer, records it in history, and returns the new version.
func (b *Buffer) Apply(op Op) uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()

	switch op.Type {
	case OpInsert:
		b.insert(op.InsertLine, op.InsertCol, op.InsertText)
		b.dirty = true
	case OpDelete:
		b.delete(op.FromLine, op.FromCol, op.ToLine, op.ToCol)
		b.dirty = true
	}

	b.version++
	op.Version = b.version
	b.history = append(b.history, op)
	return b.version
}

// OpsSince returns all ops recorded after sinceVersion.
func (b *Buffer) OpsSince(sinceVersion uint64) []Op {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if sinceVersion >= b.version {
		return nil
	}
	return b.history[sinceVersion:]
}

// SetClean marks the buffer as not dirty (called after save).
func (b *Buffer) SetClean() {
	b.mu.Lock()
	b.dirty = false
	b.mu.Unlock()
}

// ---- internal mutators (called with lock held) ----

func (b *Buffer) clampPos(line, col int) (int, int) {
	if line < 0 {
		line = 0
	}
	if line >= len(b.lines) {
		line = len(b.lines) - 1
	}
	if col < 0 {
		col = 0
	}
	if col > len(b.lines[line]) {
		col = len(b.lines[line])
	}
	return line, col
}

func (b *Buffer) insert(line, col int, text string) {
	if len(b.lines) == 0 {
		b.lines = [][]rune{{}}
	}
	line, col = b.clampPos(line, col)

	runes := []rune(text)
	newlines := 0
	for _, r := range runes {
		if r == '\n' {
			newlines++
		}
	}

	if newlines == 0 {
		// Fast path: insert within a single line.
		l := b.lines[line]
		newLine := make([]rune, len(l)+len(runes))
		copy(newLine, l[:col])
		copy(newLine[col:], runes)
		copy(newLine[col+len(runes):], l[col:])
		b.lines[line] = newLine
		return
	}

	// Split text by newlines and merge into lines slice.
	parts := splitRunes(runes)
	before := b.lines[line][:col]
	after := b.lines[line][col:]

	newLines := make([][]rune, len(b.lines)+len(parts)-1)
	copy(newLines, b.lines[:line])

	first := make([]rune, len(before)+len(parts[0]))
	copy(first, before)
	copy(first[len(before):], parts[0])
	newLines[line] = first

	for i := 1; i < len(parts)-1; i++ {
		newLines[line+i] = parts[i]
	}

	last := make([]rune, len(parts[len(parts)-1])+len(after))
	copy(last, parts[len(parts)-1])
	copy(last[len(parts[len(parts)-1]):], after)
	newLines[line+len(parts)-1] = last

	copy(newLines[line+len(parts):], b.lines[line+1:])
	b.lines = newLines
}

func (b *Buffer) delete(fromLine, fromCol, toLine, toCol int) {
	if len(b.lines) == 0 {
		return
	}
	fromLine, fromCol = b.clampPos(fromLine, fromCol)
	toLine, toCol = b.clampPos(toLine, toCol)

	if fromLine == toLine {
		l := b.lines[fromLine]
		if fromCol > toCol {
			fromCol, toCol = toCol, fromCol
		}
		if toCol > len(l) {
			toCol = len(l)
		}
		newLine := make([]rune, len(l)-(toCol-fromCol))
		copy(newLine, l[:fromCol])
		copy(newLine[fromCol:], l[toCol:])
		b.lines[fromLine] = newLine
		return
	}

	if fromLine > toLine {
		fromLine, fromCol, toLine, toCol = toLine, toCol, fromLine, fromCol
	}

	before := b.lines[fromLine][:fromCol]
	after := b.lines[toLine][toCol:]
	merged := make([]rune, len(before)+len(after))
	copy(merged, before)
	copy(merged[len(before):], after)

	newLines := make([][]rune, len(b.lines)-(toLine-fromLine))
	copy(newLines, b.lines[:fromLine])
	newLines[fromLine] = merged
	copy(newLines[fromLine+1:], b.lines[toLine+1:])
	b.lines = newLines
}

// splitRunes splits a rune slice by '\n'.
func splitRunes(r []rune) [][]rune {
	var result [][]rune
	start := 0
	for i, ch := range r {
		if ch == '\n' {
			result = append(result, r[start:i])
			start = i + 1
		}
	}
	result = append(result, r[start:])
	return result
}

// RuneOffset converts a line/col to a flat rune offset into Content().
func (b *Buffer) RuneOffset(line, col int) int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	off := 0
	for i := 0; i < line && i < len(b.lines); i++ {
		off += len(b.lines[i]) + 1 // +1 for '\n'
	}
	if line < len(b.lines) {
		if col > len(b.lines[line]) {
			col = len(b.lines[line])
		}
		off += col
	}
	return off
}

// PosFromOffset converts a flat rune offset back to line/col.
func (b *Buffer) PosFromOffset(off int) Pos {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for i, l := range b.lines {
		if off <= len(l) {
			return Pos{i, off}
		}
		off -= len(l) + 1
	}
	last := len(b.lines) - 1
	if last < 0 {
		return Pos{}
	}
	return Pos{last, len(b.lines[last])}
}

// ByteLen returns the number of UTF-8 bytes in Content().
func (b *Buffer) ByteLen() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	n := 0
	for _, l := range b.lines {
		n += utf8.RuneCountInString(string(l)) + 1
	}
	if n > 0 {
		n-- // no trailing newline
	}
	return n
}
