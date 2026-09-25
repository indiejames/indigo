package app

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/indiejames/indigo/internal/client"
)

func TestVisibleRows(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		n, maxVis, height, chrom int
		want                     int
	}{
		{"height unknown: capped by maxVisible only", 40, 16, 0, 5, 16},
		{"short list, tall terminal", 3, 16, 50, 5, 3},
		{"overflowing list reserves both indicators", 40, 16, 20, 5, 13}, // 20-5-2
		{"fits exactly with no indicators", 10, 16, 15, 5, 10},
		{"would fit, but only without indicators it then needs", 12, 16, 15, 5, 8}, // 15-5=10 < 12 → overflows → 15-5-2
		{"tiny terminal keeps one item", 40, 16, 4, 5, 1},
		{"empty list", 0, 16, 20, 5, 1},
	} {
		if got := visibleRows(tc.n, tc.maxVis, tc.height, tc.chrom); got != tc.want {
			t.Errorf("%s: visibleRows(%d, %d, %d, %d) = %d, want %d", tc.name, tc.n, tc.maxVis, tc.height, tc.chrom, got, tc.want)
		}
	}
}

// On a short terminal each picker must fit on screen with its selected row in
// it. overlayCenter drops rows that fall off either edge, so a picker taller
// than the terminal lost rows at the top and bottom — possibly the selection.
func TestPickersFitAShortTerminal(t *testing.T) {
	const n, termH, cursor = 40, 14, 20
	syms := make([]client.ClientSymbol, n)
	refs := make([]client.ClientReference, n)
	popup := make([]client.ClientPopupItem, n)
	var buffers []client.Model
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("item%02d", i)
		syms[i] = client.ClientSymbol{Name: name, Kind: 12, KindLabel: "fn", Path: "/w/a.go"}
		refs[i] = client.ClientReference{Path: "/w/" + name + ".go", Line: i}
		popup[i] = client.ClientPopupItem{Label: name}
		buffers = append(buffers, client.New(&client.RPC{}, uint32(i+1), "", 0, "/w/"+name+".go", "/w", nil, false, 0))
	}
	want := fmt.Sprintf("item%02d", cursor)

	renders := map[string]func() string{
		"file symbols": func() string {
			p := newDocSymbolPicker(syms, defaultDocSymbolFilters(), 80, termH)
			p.cursor = cursor
			return p.render()
		},
		"project symbols": func() string {
			p := newSymbolPicker(1, 80, termH)
			p.results, p.cursor = syms, cursor
			return p.render()
		},
		"references": func() string {
			p := newRefPicker("References", refs, 80, termH)
			p.cursor = cursor
			return p.render()
		},
		"buffers": func() string {
			bp := newBufPicker(buffers, cursor, 80, termH)
			return bp.render()
		},
		"plugin popup": func() string {
			p := &appPluginPopup{title: "Pick", items: popup, idx: cursor, width: 80, height: termH}
			return p.render()
		},
	}
	for name, render := range renders {
		t.Run(name, func(t *testing.T) {
			out := render()
			if h := strings.Count(out, "\n") + 1; h > termH {
				t.Errorf("picker is %d rows on a %d-row terminal", h, termH)
			}
			if !strings.Contains(ansi.Strip(out), want) {
				t.Errorf("selected item %s is not visible:\n%s", want, ansi.Strip(out))
			}
		})
	}
}
