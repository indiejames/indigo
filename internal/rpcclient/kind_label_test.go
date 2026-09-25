package rpcclient

import "testing"

// Every kind the LSP spec defines (1-26) has its own label, and no two share
// one — a blank or duplicated label in the symbol pickers is a symbol whose
// kind the user cannot read.
func TestKindLabelsCoverTheSpecUniquely(t *testing.T) {
	seen := map[string]uint8{}
	for k := uint8(1); k <= 26; k++ {
		l := kindLabel(k)
		if l == "  " || len(l) != 2 {
			t.Errorf("kind %d has no two-letter label (%q)", k, l)
			continue
		}
		if prev, dup := seen[l]; dup {
			t.Errorf("kinds %d and %d share the label %q", prev, k, l)
		}
		seen[l] = k
	}
}
