package server

import (
	"runtime"
	"strings"
	"testing"

	"github.com/indiejames/indigo/internal/document"
)

// TestPluginReadLinesRefusesAReversedRange is the regression test for an
// unsigned underflow: with end < start and start inside the buffer, the
// capacity end-start wrapped to about four billion, so a plugin calling
// ReadLines(buf, 5, 3) made the server allocate 32 GiB, or die trying.
func TestPluginReadLinesRefusesAReversedRange(t *testing.T) {
	s := &editorService{buffers: map[uint32]*bufferEntry{
		1: {buf: document.New("/x.txt", strings.Repeat("line\n", 10))},
	}}

	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	lines, err := s.PluginReadLines(1, 5, 3)
	runtime.ReadMemStats(&after)

	if err != nil || len(lines) != 0 {
		t.Errorf("ReadLines(5, 3) = %q, %v; want no lines", lines, err)
	}
	if grew := after.TotalAlloc - before.TotalAlloc; grew > 1<<20 {
		t.Errorf("ReadLines(5, 3) allocated %d bytes", grew)
	}

	if lines, _ := s.PluginReadLines(1, 2, 4); len(lines) != 2 {
		t.Errorf("ReadLines(2, 4) = %q, want two lines", lines)
	}
}
