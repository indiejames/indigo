package lsp

import "testing"

// The tree is the only thing that tells a top-level const apart from a variable
// declared inside a function; flattening once dropped it, leaving every symbol
// looking the same. A root must keep an empty ContainerName and a child must
// name its parent.
func TestFlattenDocumentSymbolsRecordsParents(t *testing.T) {
	hier := []DocumentSymbol{
		{Name: "handlers", Kind: 14, Children: []DocumentSymbol{{Name: "get", Kind: 7}}}, // top-level const map
		{Name: "run", Kind: 12, Children: []DocumentSymbol{{Name: "tmp", Kind: 13}}},     // function with a local
	}
	got := flattenDocumentSymbols(hier, "file:///a.ts")
	want := map[string]string{"handlers": "", "get": "handlers", "run": "", "tmp": "run"}
	if len(got) != len(want) {
		t.Fatalf("got %d symbols, want %d", len(got), len(want))
	}
	for _, s := range got {
		if s.ContainerName != want[s.Name] {
			t.Errorf("%s: ContainerName = %q, want %q", s.Name, s.ContainerName, want[s.Name])
		}
	}
}
