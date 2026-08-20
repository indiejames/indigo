package lsp

import "testing"

// TestDiagnosticOverlapsRange covers the boundary-touching cases:
// non-zero-width ranges (selections) use strict half-open overlap, so a
// diagnostic that only touches the edge of the selection with no actually
// shared text is excluded; zero-width ranges (a plain cursor position)
// still match a diagnostic touching exactly at that point, including a
// genuinely zero-width diagnostic there.
func TestDiagnosticOverlapsRange(t *testing.T) {
	pos := func(line, ch int) Position { return Position{Line: line, Character: ch} }
	diag := func(startCh, endCh int) Diagnostic {
		return Diagnostic{Range: Range{Start: pos(0, startCh), End: pos(0, endCh)}}
	}

	tests := []struct {
		name string
		d    Diagnostic
		rng  Range
		want bool
	}{
		{
			name: "selection strictly inside diagnostic",
			d:    diag(0, 10),
			rng:  Range{Start: pos(0, 2), End: pos(0, 5)},
			want: true,
		},
		{
			name: "selection ends exactly where diagnostic starts: no shared text, excluded",
			d:    diag(5, 10),
			rng:  Range{Start: pos(0, 0), End: pos(0, 5)},
			want: false,
		},
		{
			name: "selection starts exactly where diagnostic ends: no shared text, excluded",
			d:    diag(0, 5),
			rng:  Range{Start: pos(0, 5), End: pos(0, 10)},
			want: false,
		},
		{
			name: "selection overlapping by one column: included",
			d:    diag(5, 10),
			rng:  Range{Start: pos(0, 0), End: pos(0, 6)},
			want: true,
		},
		{
			name: "cursor exactly at diagnostic's end column: included",
			d:    diag(0, 5),
			rng:  Range{Start: pos(0, 5), End: pos(0, 5)},
			want: true,
		},
		{
			name: "cursor exactly at diagnostic's start column: included",
			d:    diag(5, 10),
			rng:  Range{Start: pos(0, 5), End: pos(0, 5)},
			want: true,
		},
		{
			name: "cursor inside diagnostic: included",
			d:    diag(0, 10),
			rng:  Range{Start: pos(0, 5), End: pos(0, 5)},
			want: true,
		},
		{
			name: "cursor outside diagnostic entirely: excluded",
			d:    diag(0, 5),
			rng:  Range{Start: pos(0, 20), End: pos(0, 20)},
			want: false,
		},
		{
			name: "zero-width diagnostic exactly at cursor: included",
			d:    diag(5, 5),
			rng:  Range{Start: pos(0, 5), End: pos(0, 5)},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := diagnosticOverlapsRange(tt.d, tt.rng); got != tt.want {
				t.Errorf("diagnosticOverlapsRange(%+v, %+v) = %v, want %v", tt.d, tt.rng, got, tt.want)
			}
		})
	}
}
