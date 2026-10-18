package main

import (
	"testing"

	"github.com/indiejames/indigo/sdk"
)

func TestShiftBookmarks(t *testing.T) {
	const f = "/a.txt"
	ins := func(line, col uint32, delta int32) sdk.EditEvent {
		return sdk.EditEvent{FilePath: f, FromLine: line, FromCol: col, ToLine: line, ToCol: col, LineDelta: delta, HasRange: true}
	}
	del := func(fl, fc, tl, tc uint32) sdk.EditEvent {
		return sdk.EditEvent{FilePath: f, FromLine: fl, FromCol: fc, ToLine: tl, ToCol: tc, LineDelta: int32(fl) - int32(tl), HasRange: true}
	}
	// want: each bookmark's line afterwards, or -1 when suspended.
	for _, tc := range []struct {
		name  string
		lines []uint32
		e     sdk.EditEvent
		want  []int
	}{
		{"Enter at the end of a bookmarked line leaves it", []uint32{2}, ins(2, 5, 1), []int{2}},
		{"a line opened above a bookmarked line moves it down", []uint32{2}, ins(2, 0, 1), []int{3}},
		{"an insertion above moves it down", []uint32{2}, ins(0, 3, 2), []int{4}},
		{"an insertion below leaves it", []uint32{2}, ins(3, 0, 1), []int{2}},
		{"dd drops the line's bookmark and moves the next up", []uint32{2, 3}, del(2, 0, 3, 0), []int{-1, 2}},
		{"a join keeps the first line's bookmark", []uint32{2}, del(2, 4, 3, 0), []int{2}},
		{"a join moves the second line's bookmark up", []uint32{3}, del(2, 4, 3, 0), []int{2}},
		{"a join of two bookmarked lines keeps the first", []uint32{2, 3}, del(2, 4, 3, 0), []int{2, -1}},
		{"lines wholly inside a deletion lose theirs", []uint32{3, 4, 5, 9}, del(2, 1, 5, 2), []int{-1, -1, 2, 6}},
		{"another file is untouched", []uint32{2}, sdk.EditEvent{FilePath: "/b.txt", ToLine: 5, LineDelta: -5, HasRange: true}, []int{2}},
		{"with no range, the line-only rule applies", []uint32{2, 3, 5}, sdk.EditEvent{FilePath: f, FromLine: 2, LineDelta: -2}, []int{-1, -1, 3}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var bms []bookmark
			for _, l := range tc.lines {
				bms = append(bms, bookmark{filePath: f, line: l, active: true})
			}
			shiftBookmarks(bms, tc.e)
			for i, bm := range bms {
				got := int(bm.line)
				if !bm.active {
					got = -1
				}
				if got != tc.want[i] {
					t.Errorf("bookmark on %d: got %d, want %d", tc.lines[i], got, tc.want[i])
				}
			}
		})
	}
}
