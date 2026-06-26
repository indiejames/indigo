package document

const gapInitSize = 256

// gapBuf is a gap buffer for the active editing region.
//
// Logical content = buf[0..gapStart) ++ buf[gapEnd..)
// nlCount caches the number of '\n' in the logical content.
type gapBuf struct {
	buf      []rune
	gapStart int
	gapEnd   int
	nlCount  int
}

func (g *gapBuf) runeLen() int {
	return g.gapStart + (len(g.buf) - g.gapEnd)
}

// moveGapTo moves the gap so that gapStart == pos (a logical content position).
func (g *gapBuf) moveGapTo(pos int) {
	n := g.runeLen()
	if pos < 0 {
		pos = 0
	}
	if pos > n {
		pos = n
	}
	if pos == g.gapStart {
		return
	}
	gapLen := g.gapEnd - g.gapStart
	if pos < g.gapStart {
		// Slide gap left: copy buf[pos..gapStart) to buf[gapEnd-(gapStart-pos)..gapEnd)
		shift := g.gapStart - pos
		copy(g.buf[g.gapEnd-shift:g.gapEnd], g.buf[pos:g.gapStart])
		g.gapStart = pos
		g.gapEnd = pos + gapLen
	} else {
		// Slide gap right: copy buf[gapEnd..gapEnd+(pos-gapStart)) to buf[gapStart..pos)
		shift := pos - g.gapStart
		copy(g.buf[g.gapStart:pos], g.buf[g.gapEnd:g.gapEnd+shift])
		g.gapStart = pos
		g.gapEnd = pos + gapLen
	}
}

func (g *gapBuf) grow(needed int) {
	newGapSize := max(needed+gapInitSize, gapInitSize)
	n := g.runeLen()
	newBuf := make([]rune, n+newGapSize)
	copy(newBuf, g.buf[:g.gapStart])
	copy(newBuf[g.gapStart+newGapSize:], g.buf[g.gapEnd:])
	g.gapEnd = g.gapStart + newGapSize
	g.buf = newBuf
}

// insertAt inserts runes at logical gap position pos.
func (g *gapBuf) insertAt(pos int, runes []rune) {
	if len(runes) == 0 {
		return
	}
	g.moveGapTo(pos)
	if len(runes) > g.gapEnd-g.gapStart {
		g.grow(len(runes))
	}
	for _, r := range runes {
		if r == '\n' {
			g.nlCount++
		}
	}
	copy(g.buf[g.gapStart:], runes)
	g.gapStart += len(runes)
}

// deleteAt deletes count runes starting at logical position pos.
func (g *gapBuf) deleteAt(pos, count int) {
	if count <= 0 {
		return
	}
	g.moveGapTo(pos)
	// Runes to delete are now at buf[gapEnd..gapEnd+count).
	end := min(g.gapEnd+count, len(g.buf))
	for i := g.gapEnd; i < end; i++ {
		if g.buf[i] == '\n' {
			g.nlCount--
		}
	}
	g.gapEnd += count
	if g.gapEnd > len(g.buf) {
		g.gapEnd = len(g.buf)
	}
}

// reset empties the gap buffer, reusing the backing array as all-gap.
func (g *gapBuf) reset() {
	g.gapStart = 0
	g.gapEnd = len(g.buf)
	g.nlCount = 0
}

// content returns the full logical content as a new slice.
func (g *gapBuf) content() []rune {
	n := g.runeLen()
	if n == 0 {
		return nil
	}
	out := make([]rune, n)
	copy(out, g.buf[:g.gapStart])
	copy(out[g.gapStart:], g.buf[g.gapEnd:])
	return out
}

// contentSlice returns logical content [from, to) as a new slice.
func (g *gapBuf) contentSlice(from, to int) []rune {
	if from >= to {
		return nil
	}
	out := make([]rune, to-from)
	gapLen := g.gapEnd - g.gapStart
	for i := from; i < to; i++ {
		if i < g.gapStart {
			out[i-from] = g.buf[i]
		} else {
			out[i-from] = g.buf[i+gapLen]
		}
	}
	return out
}

// newlinesBefore counts '\n' in logical content [0..to).
func (g *gapBuf) newlinesBefore(to int) int {
	if to <= 0 {
		return 0
	}
	count := 0
	gapLen := g.gapEnd - g.gapStart
	n := g.runeLen()
	if to > n {
		to = n
	}
	for i := 0; i < to; i++ {
		var r rune
		if i < g.gapStart {
			r = g.buf[i]
		} else {
			r = g.buf[i+gapLen]
		}
		if r == '\n' {
			count++
		}
	}
	return count
}

// offsetOfNthNewline returns the logical offset of the n-th '\n' (0-indexed), or -1.
func (g *gapBuf) offsetOfNthNewline(n int) int {
	count := 0
	total := g.runeLen()
	gapLen := g.gapEnd - g.gapStart
	for i := 0; i < total; i++ {
		var r rune
		if i < g.gapStart {
			r = g.buf[i]
		} else {
			r = g.buf[i+gapLen]
		}
		if r == '\n' {
			if count == n {
				return i
			}
			count++
		}
	}
	return -1
}
