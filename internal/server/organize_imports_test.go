package server

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/indiejames/indigo/internal/document"
	"github.com/indiejames/indigo/internal/lint"
	"github.com/indiejames/indigo/internal/lsp"
	proto "github.com/indiejames/indigo/internal/proto"
)

func TestLspEditsForURIPrefersDocumentChanges(t *testing.T) {
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

	got := lspEditsForURI(edit, "file:///a.go")
	if len(got) != 1 || got[0].NewText != "from documentChanges" {
		t.Errorf("lspEditsForURI() = %+v, want documentChanges edits", got)
	}
}

func TestLspEditsForURIFallsBackToChanges(t *testing.T) {
	edit := &lsp.WorkspaceEdit{
		Changes: map[string][]lsp.TextEdit{
			"file:///a.go": {{NewText: "from changes"}},
		},
	}

	got := lspEditsForURI(edit, "file:///a.go")
	if len(got) != 1 || got[0].NewText != "from changes" {
		t.Errorf("lspEditsForURI() = %+v, want the changes map entry", got)
	}
}

func TestLspEditsForURINilEditOrNoMatch(t *testing.T) {
	if got := lspEditsForURI(nil, "file:///a.go"); got != nil {
		t.Errorf("lspEditsForURI(nil, ...) = %+v, want nil", got)
	}
	edit := &lsp.WorkspaceEdit{Changes: map[string][]lsp.TextEdit{"file:///other.go": {{NewText: "x"}}}}
	if got := lspEditsForURI(edit, "file:///a.go"); got != nil {
		t.Errorf("lspEditsForURI() for a non-matching uri = %+v, want nil", got)
	}
}

// TestLspOrganizeImportsUnknownBuffer verifies the RPC rejects a bufId with
// no corresponding buffer, matching every other bufId-keyed handler.
func TestLspOrganizeImportsUnknownBuffer(t *testing.T) {
	s := &editorService{
		buffers:     map[uint32]*bufferEntry{},
		lspMgr:      lsp.NewManager(t.TempDir(), nil),
		lintMgr:     &lint.Manager{},
		dirWatches:  make(map[string]int),
		savingPaths: make(map[string]time.Time),
	}
	client := proto.EditorService_ServerToClient(&connSvc{editorService: s, connID: 1})

	fut, rel := client.LspOrganizeImports(context.Background(), func(p proto.EditorService_lspOrganizeImports_Params) error {
		p.SetBufId(99)
		return nil
	})
	defer rel()
	if _, err := fut.Struct(); err == nil {
		t.Fatal("expected an error for an unknown buffer, got nil")
	}
}

// TestLspOrganizeImportsNoLanguageServerReturnsEmpty verifies the RPC round
// trips cleanly through the capnp boundary and returns an empty edit list
// (not an error) when no language server is attached for the buffer's path
// — the common case for a file type indigo has no LSP config for.
func TestLspOrganizeImportsNoLanguageServerReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "main.go")
	s := &editorService{
		buffers: map[uint32]*bufferEntry{
			1: {buf: document.New(path, "package main\n"), canonPath: canonicalPath(path)},
		},
		lspMgr:      lsp.NewManager(dir, nil),
		lintMgr:     &lint.Manager{},
		dirWatches:  make(map[string]int),
		savingPaths: make(map[string]time.Time),
	}
	client := proto.EditorService_ServerToClient(&connSvc{editorService: s, connID: 1})

	fut, rel := client.LspOrganizeImports(context.Background(), func(p proto.EditorService_lspOrganizeImports_Params) error {
		p.SetBufId(1)
		return nil
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		t.Fatalf("LspOrganizeImports returned an error: %v", err)
	}
	edits, err := res.Edits()
	if err != nil {
		t.Fatalf("res.Edits(): %v", err)
	}
	if edits.Len() != 0 {
		t.Errorf("edits.Len() = %d, want 0 (no language server configured)", edits.Len())
	}
}
