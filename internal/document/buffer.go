package document

import (
	"crypto/sha256"
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

// Buffer holds the text of a single file using a hybrid rope + gap buffer.
//
// Logical content = rope[0..gapOffset) ++ gap.content() ++ rope[gapOffset..)
//
// The rope stores only committed content; the gap buffer overlays it at gapOffset.
// All public methods are safe for concurrent use.
type Buffer struct {
	mu        sync.RWMutex
	rope      *ropeNode // committed content, does not include gap runes
	gap       gapBuf    // active editing overlay
	gapOffset int       // where in the rope the gap is inserted
	version   uint64
	history   []Op
	// historyBase is the version of the op immediately before history[0] —
	// i.e. how many ops have been reclaimed by TrimHistory. 0 until the
	// first trim, at which point history[0].Version == historyBase+1.
	historyBase uint64
	path        string
	dirty       bool
	savedHash   [32]byte // sha256 of content at last load or save
}

// New creates a Buffer from raw file content.
func New(path, content string) *Buffer {
	return &Buffer{
		path:      path,
		rope:      ropeFromRunes([]rune(content)),
		savedHash: sha256.Sum256([]byte(content)),
	}
}

func (b *Buffer) Path() string { return b.path }

func (b *Buffer) Dirty() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.dirty
}

func (b *Buffer) Version() uint64 {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.version
}

func (b *Buffer) SetClean() {
	b.mu.Lock()
	b.savedHash = sha256.Sum256([]byte(string(b.logicalSlice(0, b.totalLen()))))
	b.dirty = false
	b.mu.Unlock()
}

func (b *Buffer) SavedHash() [32]byte {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.savedHash
}

func (b *Buffer) MarkDirty() {
	b.mu.Lock()
	b.dirty = true
	b.mu.Unlock()
}

// ---- internal helpers (called with lock held) ----

func (b *Buffer) totalLen() int {
	return ropeLen(b.rope) + b.gap.runeLen()
}

func (b *Buffer) lineCount() int {
	return ropeNewlines(b.rope) + b.gap.nlCount + 1
}

// newlinesBefore counts '\n' in logical content [0..off).
func (b *Buffer) newlinesBefore(off int) int {
	if off <= 0 {
		return 0
	}
	gapLen := b.gap.runeLen()
	gapEnd := b.gapOffset + gapLen

	// Zone A: rope[0..min(off, gapOffset))
	aEnd := min(off, b.gapOffset)
	nlA := b.rope.newlinesBefore(aEnd)
	if off <= b.gapOffset {
		return nlA
	}

	// Zone B: gap content [0..min(off-gapOffset, gapLen))
	bEnd := min(off-b.gapOffset, gapLen)
	nlB := b.gap.newlinesBefore(bEnd)
	if off <= gapEnd {
		return nlA + nlB
	}

	// Zone C: rope[gapOffset..off-gapLen)
	nlBeforeGap := b.rope.newlinesBefore(b.gapOffset)
	nlC := b.rope.newlinesBefore(off-gapLen) - nlBeforeGap
	return nlA + nlB + nlC
}

// offsetOfNthNewlineLogical returns the logical offset of the n-th '\n' (0-indexed), or -1.
func (b *Buffer) offsetOfNthNewlineLogical(n int) int {
	total := ropeNewlines(b.rope) + b.gap.nlCount
	if n < 0 || n >= total {
		return -1
	}
	nlBeforeGap := b.rope.newlinesBefore(b.gapOffset)
	gapLen := b.gap.runeLen()

	if n < nlBeforeGap {
		// n-th newline is in rope before gap; rope offset == logical offset here.
		return b.rope.offsetOfNthNewline(n)
	}

	nInGap := n - nlBeforeGap
	if nInGap < b.gap.nlCount {
		// n-th newline is in gap content.
		return b.gapOffset + b.gap.offsetOfNthNewline(nInGap)
	}

	// n-th newline is in rope after gap.
	// In the full rope this is newline index (n - gap.nlCount).
	ropeOff := b.rope.offsetOfNthNewline(n - b.gap.nlCount)
	if ropeOff < 0 {
		return -1
	}
	return ropeOff + gapLen
}

// logicalOffset converts (line, col) to a flat logical rune offset, clamped to valid range.
func (b *Buffer) logicalOffset(line, col int) int {
	total := b.totalLen()
	nl := b.lineCount() - 1

	if line < 0 {
		line = 0
	}
	if line > nl {
		line = nl
	}

	var lineStart int
	if line > 0 {
		off := b.offsetOfNthNewlineLogical(line - 1)
		if off < 0 {
			return total
		}
		lineStart = off + 1
	}

	var lineEnd int
	if line >= nl {
		lineEnd = total
	} else {
		lineEnd = b.offsetOfNthNewlineLogical(line)
		if lineEnd < 0 {
			lineEnd = total
		}
	}

	if col < 0 {
		col = 0
	}
	pos := lineStart + col
	if pos > lineEnd {
		pos = lineEnd
	}
	return pos
}

// logicalSlice extracts runes from logical positions [from, to).
func (b *Buffer) logicalSlice(from, to int) []rune {
	gapLen := b.gap.runeLen()
	gapEnd := b.gapOffset + gapLen
	total := ropeLen(b.rope) + gapLen

	from = max(0, from)
	to = min(to, total)
	if from >= to {
		return nil
	}

	out := make([]rune, 0, to-from)

	// Zone A: rope before gap
	aEnd := min(to, b.gapOffset)
	if from < aEnd {
		out = append(out, b.rope.extractRange(from, aEnd)...)
	}

	// Zone B: gap content
	bStart := max(from, b.gapOffset)
	bEnd := min(to, gapEnd)
	if bStart < bEnd {
		out = append(out, b.gap.contentSlice(bStart-b.gapOffset, bEnd-b.gapOffset)...)
	}

	// Zone C: rope after gap (logical → rope: subtract gapLen)
	cStart := max(from, gapEnd)
	if cStart < to {
		out = append(out, b.rope.extractRange(cStart-gapLen, to-gapLen)...)
	}

	return out
}

func (b *Buffer) internalLine(n int) string {
	nl := b.lineCount() - 1
	if n < 0 || n > nl {
		return ""
	}
	var lineStart int
	if n > 0 {
		off := b.offsetOfNthNewlineLogical(n - 1)
		if off < 0 {
			return ""
		}
		lineStart = off + 1
	}
	var lineEnd int
	if n >= nl {
		lineEnd = b.totalLen()
	} else {
		lineEnd = b.offsetOfNthNewlineLogical(n)
		if lineEnd < 0 {
			lineEnd = b.totalLen()
		}
	}
	return string(b.logicalSlice(lineStart, lineEnd))
}

func (b *Buffer) internalLineLen(n int) int {
	nl := b.lineCount() - 1
	if n < 0 || n > nl {
		return 0
	}
	var lineStart int
	if n > 0 {
		off := b.offsetOfNthNewlineLogical(n - 1)
		if off < 0 {
			return 0
		}
		lineStart = off + 1
	}
	var lineEnd int
	if n >= nl {
		lineEnd = b.totalLen()
	} else {
		lineEnd = b.offsetOfNthNewlineLogical(n)
		if lineEnd < 0 {
			lineEnd = b.totalLen()
		}
	}
	return lineEnd - lineStart
}

// ---- gap management ----

func (b *Buffer) flushGap() {
	if b.gap.runeLen() == 0 {
		return
	}
	b.rope = ropeInsert(b.rope, b.gapOffset, b.gap.content())
	b.gap.reset()
}

func (b *Buffer) insertAt(pos int, runes []rune) {
	if len(runes) == 0 {
		return
	}
	gapEnd := b.gapOffset + b.gap.runeLen()
	if pos >= b.gapOffset && pos <= gapEnd {
		b.gap.insertAt(pos-b.gapOffset, runes)
	} else {
		b.flushGap()
		b.gapOffset = pos
		b.gap.insertAt(0, runes)
	}
}

func (b *Buffer) deleteRange(fromPos, toPos int) {
	if fromPos >= toPos {
		return
	}
	gapLen := b.gap.runeLen()
	gapEnd := b.gapOffset + gapLen

	// Entirely within gap content.
	if fromPos >= b.gapOffset && toPos <= gapEnd {
		b.gap.deleteAt(fromPos-b.gapOffset, toPos-fromPos)
		return
	}
	// Entirely in rope before gap (no gap involvement).
	if toPos <= b.gapOffset {
		b.rope = ropeDelete(b.rope, fromPos, toPos)
		b.gapOffset -= toPos - fromPos
		return
	}
	// Entirely in rope after gap.
	if fromPos >= gapEnd {
		b.rope = ropeDelete(b.rope, fromPos-gapLen, toPos-gapLen)
		return
	}
	// Spans gap: flush first, then delete from the unified rope.
	b.flushGap()
	b.rope = ropeDelete(b.rope, fromPos, toPos)
	b.gapOffset = fromPos
}

// ---- public API ----

func (b *Buffer) LineCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.lineCount()
}

func (b *Buffer) Line(n int) string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.internalLine(n)
}

func (b *Buffer) LineLen(n int) int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.internalLineLen(n)
}

func (b *Buffer) Content() string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return string(b.logicalSlice(0, b.totalLen()))
}

func (b *Buffer) Apply(op Op) uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()

	switch op.Type {
	case OpInsert:
		pos := b.logicalOffset(op.InsertLine, op.InsertCol)
		b.insertAt(pos, []rune(op.InsertText))
		b.dirty = true
	case OpDelete:
		fromPos := b.logicalOffset(op.FromLine, op.FromCol)
		toPos := b.logicalOffset(op.ToLine, op.ToCol)
		if fromPos > toPos {
			fromPos, toPos = toPos, fromPos
		}
		b.deleteRange(fromPos, toPos)
		b.dirty = true
	}

	b.version++
	op.Version = b.version
	b.history = append(b.history, op)
	return b.version
}

func (b *Buffer) OpsSince(sinceVersion uint64) []Op {
	ops, _ := b.OpsSinceAndVersion(sinceVersion)
	return ops
}

// OpsSinceAndVersion returns the same ops OpsSince would, plus the buffer's
// version, both read under one lock acquisition. Callers that report a
// version alongside an ops list (like GetUpdates) must use this rather than
// calling OpsSince and Version separately: a concurrent Apply landing
// between the two calls would let the reported version race ahead of what
// the ops list actually covers, and the caller believing itself caught up
// to that version would never receive the op that got skipped.
func (b *Buffer) OpsSinceAndVersion(sinceVersion uint64) ([]Op, uint64) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if sinceVersion >= b.version {
		return nil, b.version
	}
	if sinceVersion < b.historyBase {
		// Ops this old were already reclaimed by TrimHistory; a correctly
		// behaving caller never asks for less than what TrimHistory left
		// available (see recordClientProgress in server_buffer.go), so this
		// is a defensive clamp rather than an expected path.
		sinceVersion = b.historyBase
	}
	return b.history[sinceVersion-b.historyBase:], b.version
}

// HistoryLen reports how many ops are currently retained (post-trim).
func (b *Buffer) HistoryLen() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.history)
}

// TrimHistory discards retained ops at or before minVersion, keeping only
// ops after it. Callers must only pass a minVersion that every consumer of
// OpsSince has already caught up past (see recordClientProgress), since
// trimmed ops are gone for good.
func (b *Buffer) TrimHistory(minVersion uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if minVersion <= b.historyBase {
		return
	}
	idx := minVersion - b.historyBase
	if idx > uint64(len(b.history)) {
		idx = uint64(len(b.history))
	}
	trimmed := make([]Op, len(b.history)-int(idx))
	copy(trimmed, b.history[idx:])
	b.history = trimmed
	b.historyBase += idx
}

func (b *Buffer) RuneOffset(line, col int) int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.logicalOffset(line, col)
}

func (b *Buffer) PosFromOffset(off int) Pos {
	b.mu.RLock()
	defer b.mu.RUnlock()
	total := b.totalLen()
	if off <= 0 {
		return Pos{}
	}
	if off >= total {
		last := b.lineCount() - 1
		return Pos{last, b.internalLineLen(last)}
	}
	nl := b.newlinesBefore(off)
	if nl == 0 {
		return Pos{0, off}
	}
	lineStart := b.offsetOfNthNewlineLogical(nl-1) + 1
	return Pos{nl, off - lineStart}
}

// ByteLen returns the number of UTF-8 bytes in Content().
func (b *Buffer) ByteLen() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	n := 0
	for _, r := range b.logicalSlice(0, b.totalLen()) {
		n += utf8.RuneLen(r)
	}
	return n
}
