package app

import (
	"fmt"
	"strings"
	"testing"

	"github.com/indiejames/indigo/internal/client"
)

// A picker whose list overflows used to add its "↑ more" / "↓ more" rows only
// when there was something that way, so it was a row taller in the middle of
// the list than at either end and jumped as the cursor moved. Both rows are
// now always reserved, so the height is the same wherever the cursor is.
func TestPickerHeightIsStableWhileScrolling(t *testing.T) {
	const n = 40 // well past the 16 visible

	syms := make([]client.ClientSymbol, n)
	refs := make([]client.ClientReference, n)
	for i := range syms {
		syms[i] = client.ClientSymbol{Name: fmt.Sprintf("s%02d", i), Kind: 12, KindLabel: "fn"}
		refs[i] = client.ClientReference{Path: "/w/a.go", Line: i, Col: 0}
	}

	for name, render := range map[string]func(cursor int) string{
		"document symbols": func(c int) string {
			p := newDocSymbolPicker(syms, defaultDocSymbolFilters(), 100, 30)
			p.cursor = c
			return p.render()
		},
		"references": func(c int) string {
			p := newRefPicker("References", refs, 100, 30)
			p.cursor = c
			return p.render()
		},
	} {
		t.Run(name, func(t *testing.T) {
			heights := map[int]int{}
			for _, c := range []int{0, n / 2, n - 1} {
				heights[c] = strings.Count(render(c), "\n") + 1
			}
			if heights[0] != heights[n/2] || heights[n/2] != heights[n-1] {
				t.Errorf("height by cursor position = %v; want one height", heights)
			}
			if top := render(0); strings.Contains(top, "↑ more") || !strings.Contains(top, "↓ more") {
				t.Error("at the top the indicators should read: blank above, ↓ more below")
			}
		})
	}
}
