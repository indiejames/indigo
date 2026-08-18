package client

import (
	"context"
	"testing"

	proto "github.com/indiejames/indigo/internal/proto"
)

// fakeResolveCompletionService fakes just the ResolveCompletion RPC method;
// every other EditorService method is left unimplemented (embedded nil
// interface) since these tests never invoke them.
type fakeResolveCompletionService struct {
	proto.EditorService_Server
	resolve func(call proto.EditorService_resolveCompletion) error
}

func (f *fakeResolveCompletionService) ResolveCompletion(_ context.Context, call proto.EditorService_resolveCompletion) error {
	return f.resolve(call)
}

// TestResolveCompletionRetainsPreResolveTextEditWhenResponseOmitsIt is a
// regression test: RPC.ResolveCompletion's outbound request never carries the
// item's TextEdit (see its doc comment), so a resolver with no way to
// reconstruct one it was never given — a plugin, unlike a language server
// with its own cached state — legitimately can't echo it back. Without this
// retention, applyCompletionItem would fall back to replacing only the
// typed-prefix range instead of the original TextEdit's full range.
func TestResolveCompletionRetainsPreResolveTextEditWhenResponseOmitsIt(t *testing.T) {
	fake := &fakeResolveCompletionService{resolve: func(call proto.EditorService_resolveCompletion) error {
		res, err := call.AllocResults()
		if err != nil {
			return err
		}
		out, err := res.NewItem()
		if err != nil {
			return err
		}
		if err := out.SetLabel("1.2.3"); err != nil {
			return err
		}
		// Deliberately no TextEdit on the response.
		return nil
	}}
	client := proto.EditorService_ServerToClient(fake)
	t.Cleanup(client.Release)
	r := &RPC{svc: client}

	original := ClientCompletion{
		Label: "1.2.3",
		Data:  []byte("token"),
		TextEdit: &ClientLspEdit{
			FromLine: 4, FromCol: 1, ToLine: 4, ToCol: 5, NewText: "1.2.3",
		},
	}

	resolved, err := r.ResolveCompletion(context.Background(), 1, original)
	if err != nil {
		t.Fatalf("ResolveCompletion: %v", err)
	}
	if resolved.TextEdit == nil {
		t.Fatal("resolved.TextEdit = nil, want the pre-resolve TextEdit retained")
	}
	if *resolved.TextEdit != *original.TextEdit {
		t.Errorf("resolved.TextEdit = %+v, want %+v", *resolved.TextEdit, *original.TextEdit)
	}
}

// TestResolveCompletionUsesResolvedTextEditWhenPresent verifies the response
// takes priority when it does supply its own TextEdit (the existing LSP
// auto-import path relies on this — the resolved item can differ from what
// was originally fetched).
func TestResolveCompletionUsesResolvedTextEditWhenPresent(t *testing.T) {
	fake := &fakeResolveCompletionService{resolve: func(call proto.EditorService_resolveCompletion) error {
		res, err := call.AllocResults()
		if err != nil {
			return err
		}
		out, err := res.NewItem()
		if err != nil {
			return err
		}
		if err := out.SetLabel("1.2.3"); err != nil {
			return err
		}
		te, err := out.NewTextEdit()
		if err != nil {
			return err
		}
		te.SetFromLine(9)
		te.SetFromCol(9)
		te.SetToLine(9)
		te.SetToCol(9)
		if err := te.SetNewText("resolved-edit"); err != nil {
			return err
		}
		return nil
	}}
	client := proto.EditorService_ServerToClient(fake)
	t.Cleanup(client.Release)
	r := &RPC{svc: client}

	original := ClientCompletion{
		Label: "1.2.3",
		TextEdit: &ClientLspEdit{
			FromLine: 4, FromCol: 1, ToLine: 4, ToCol: 5, NewText: "1.2.3",
		},
	}

	resolved, err := r.ResolveCompletion(context.Background(), 1, original)
	if err != nil {
		t.Fatalf("ResolveCompletion: %v", err)
	}
	if resolved.TextEdit == nil || resolved.TextEdit.NewText != "resolved-edit" {
		t.Errorf("resolved.TextEdit = %+v, want the resolved response's own TextEdit (NewText = %q)",
			resolved.TextEdit, "resolved-edit")
	}
}
