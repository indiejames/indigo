package agenttools

import (
	"testing"

	"github.com/indiejames/indigo/internal/rpcclient"
)

// gopls names a method "Type.method" in workspace symbols, so an exact match
// on the bare name found no Go method at all: find_definition("markSaving")
// failed with "no symbol is named exactly" while "editorService.markSaving"
// worked.
func TestMatchSymbolNameFindsQualifiedMethods(t *testing.T) {
	sym := func(name, path string) rpcclient.ClientSymbol {
		return rpcclient.ClientSymbol{Name: name, Path: path}
	}
	names := func(got []rpcclient.ClientSymbol) []string {
		var out []string
		for _, s := range got {
			out = append(out, s.Name+"@"+s.Path)
		}
		return out
	}

	t.Run("bare name finds a gopls-qualified method", func(t *testing.T) {
		syms := []rpcclient.ClientSymbol{
			sym("editorService.markSaving", "server.go"),
			sym("editorService.unmarkSaving", "server.go"),
		}
		got := matchSymbolName(syms, "markSaving")
		if len(got) != 1 || got[0].Name != "editorService.markSaving" {
			t.Errorf("got %v, want only editorService.markSaving", names(got))
		}
	})

	t.Run("pointer-receiver spelling matches too", func(t *testing.T) {
		got := matchSymbolName([]rpcclient.ClientSymbol{sym("(*Buffer).Apply", "b.go")}, "Apply")
		if len(got) != 1 {
			t.Errorf("got %v, want (*Buffer).Apply", names(got))
		}
	})

	t.Run("an exact match takes precedence over methods", func(t *testing.T) {
		syms := []rpcclient.ClientSymbol{
			sym("conn.Close", "a.go"),
			sym("Close", "b.go"),
		}
		got := matchSymbolName(syms, "Close")
		if len(got) != 1 || got[0].Path != "b.go" {
			t.Errorf("got %v, want only the function Close", names(got))
		}
	})

	t.Run("several methods of that name are all returned", func(t *testing.T) {
		syms := []rpcclient.ClientSymbol{sym("conn.Close", "a.go"), sym("file.Close", "b.go")}
		if got := matchSymbolName(syms, "Close"); len(got) != 2 {
			t.Errorf("got %v, want both methods", names(got))
		}
	})

	t.Run("a substring that is not the method name does not match", func(t *testing.T) {
		syms := []rpcclient.ClientSymbol{sym("editorService.unmarkSaving", "server.go")}
		if got := matchSymbolName(syms, "markSaving"); len(got) != 0 {
			t.Errorf("got %v, want no match", names(got))
		}
	})

	t.Run("a qualified query still needs an exact match", func(t *testing.T) {
		syms := []rpcclient.ClientSymbol{sym("other.editorService.markSaving", "x.go")}
		if got := matchSymbolName(syms, "editorService.markSaving"); len(got) != 0 {
			t.Errorf("got %v, want no match", names(got))
		}
	})
}
