package app

import (
	"strings"
	"testing"

	"github.com/indiejames/indigo/internal/client"
)

// The references picker used an orange-and-rust palette that read as an error
// dialog. It now shares the "Go to Symbol in File" picker's colours. Checked on
// the rendered output, so a stray hard-coded colour anywhere in the render
// path is caught, not just in the style variables.
func TestRefPickerUsesTheSymbolPickerColors(t *testing.T) {
	refs := []client.ClientReference{{Path: "/w/a.go", Line: 3, Preview: "foo()"}, {Path: "/w/b.go", Line: 9}}
	out := newRefPicker("References", refs, 100, 30).render()

	// 24-bit colour parameters as lipgloss writes them.
	old := map[string]string{
		"#AA6644 border": "170;102;68",
		"#DDAA88 title":  "221;170;136",
		"#6A3010 select": "106;48;16",
		"#AA7755 loc":    "170;119;85",
	}
	for name, rgb := range old {
		if strings.Contains(out, rgb) {
			t.Errorf("old colour %s is still used", name)
		}
	}
	if !strings.Contains(out, "68;170;204") { // #44AACC, the symbol picker's border
		t.Error("the symbol picker's border colour is not used")
	}
	if !strings.Contains(out, "45;95;138") { // #2D5F8A, the symbol picker's selected row
		t.Error("the symbol picker's selection colour is not used")
	}
}
