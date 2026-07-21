package server

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/indiejames/indigo/internal/document"
	"github.com/indiejames/indigo/internal/lsp"
)

func TestLspEditsByURIPrefersDocumentChanges(t *testing.T) {
	edit := &lsp.WorkspaceEdit{
		Changes: map[string][]lsp.TextEdit{
			"file:///a.go": {{NewText: "from changes"}},
		},
		DocumentChanges: []lsp.TextDocumentEdit{
			{
				TextDocument: struct {
					URI string `json:"uri"`
				}{URI: "file:///a.go"},
				Edits: []lsp.TextEdit{{NewText: "from documentChanges"}},
			},
		},
	}

	got := lspEditsByURI(edit)
	if len(got) != 1 || got["file:///a.go"][0].NewText != "from documentChanges" {
		t.Errorf("lspEditsByURI() = %+v, want documentChanges edits", got)
	}
}

func TestLspEditsByURIFallsBackToChanges(t *testing.T) {
	edit := &lsp.WorkspaceEdit{
		Changes: map[string][]lsp.TextEdit{
			"file:///a.go": {{NewText: "from changes"}},
			"file:///b.go": {{NewText: "also from changes"}},
		},
	}

	got := lspEditsByURI(edit)
	if len(got) != 2 || got["file:///a.go"][0].NewText != "from changes" {
		t.Errorf("lspEditsByURI() = %+v, want the changes map", got)
	}
}

func TestLspEditsByURINil(t *testing.T) {
	if got := lspEditsByURI(nil); got != nil {
		t.Errorf("lspEditsByURI(nil) = %+v, want nil", got)
	}
}

func rangeAt(line, startCol, endCol int) lsp.Range {
	return lsp.Range{
		Start: lsp.Position{Line: line, Character: startCol},
		End:   lsp.Position{Line: line, Character: endCol},
	}
}

func TestWorkspaceEditItemsFromLSPReadsOpenBuffer(t *testing.T) {
	s := &editorService{
		buffers: map[uint32]*bufferEntry{
			1: {buf: document.New("/a.go", "foo bar\nfoo baz\n")},
		},
	}

	items, err := s.workspaceEditItemsFromLSP("/a.go", []lsp.TextEdit{
		{Range: rangeAt(0, 0, 3), NewText: "quux"},
		{Range: rangeAt(1, 0, 3), NewText: "quux"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("len(items) = %d, want 2", len(items))
	}
	if items[0].oldText != "foo" || items[0].newText != "quux" || items[0].line != 0 || items[0].col != 0 {
		t.Errorf("items[0] = %+v, want oldText=foo newText=quux line=0 col=0", items[0])
	}
	if items[1].line != 1 {
		t.Errorf("items[1].line = %d, want 1", items[1].line)
	}
}

func TestWorkspaceEditItemsFromLSPReadsDisk(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.go")
	if err := os.WriteFile(path, []byte("hello world\n"), 0644); err != nil {
		t.Fatal(err)
	}

	s := &editorService{}
	items, err := s.workspaceEditItemsFromLSP(path, []lsp.TextEdit{
		{Range: rangeAt(0, 0, 5), NewText: "howdy"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].oldText != "hello" || items[0].newText != "howdy" {
		t.Errorf("items = %+v, want one edit hello->howdy", items)
	}
}

func TestWorkspaceEditItemsFromLSPSkipsMultiLineEdit(t *testing.T) {
	s := &editorService{
		buffers: map[uint32]*bufferEntry{
			1: {buf: document.New("/a.go", "foo\nbar\n")},
		},
	}

	items, err := s.workspaceEditItemsFromLSP("/a.go", []lsp.TextEdit{
		{Range: lsp.Range{Start: lsp.Position{Line: 0, Character: 0}, End: lsp.Position{Line: 1, Character: 3}}, NewText: "x"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Errorf("items = %+v, want none (multi-line edit skipped)", items)
	}
}

func TestWorkspaceEditItemsFromLSPSkipsOutOfRangeEdit(t *testing.T) {
	s := &editorService{
		buffers: map[uint32]*bufferEntry{
			1: {buf: document.New("/a.go", "foo\n")},
		},
	}

	items, err := s.workspaceEditItemsFromLSP("/a.go", []lsp.TextEdit{
		{Range: rangeAt(5, 0, 3), NewText: "x"},  // line out of range
		{Range: rangeAt(0, 0, 99), NewText: "x"}, // col out of range
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Errorf("items = %+v, want none (both edits out of range)", items)
	}
}
