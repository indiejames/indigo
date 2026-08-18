package sdk

import (
	"context"
	"testing"

	"github.com/indiejames/indigo/internal/proto/pluginproto"
)

// captureCompletionProvider wires a fake RegisterCompletionProvider that
// records the provider capability the SDK sends, so tests can drive its
// GetCompletions/ResolveCompletion methods directly — the same "capture the
// handler capability, then call back into it" pattern api_methods_test.go
// uses for ShowPopup/ShowInputPrompt's handler dispatch.
func captureCompletionProvider(t *testing.T, h CompletionHandlers) pluginproto.CompletionProvider {
	t.Helper()
	var captured pluginproto.CompletionProvider
	fake := &fakeFullEditorApi{registerCompletionProvider: func(call pluginproto.EditorApi_registerCompletionProvider) error {
		captured = call.Args().Provider().AddRef()
		_, err := call.AllocResults()
		return err
	}}
	api := newTestApi(t, fake)
	if err := api.CompletionsFull(h); err != nil {
		t.Fatalf("CompletionsFull: %v", err)
	}
	t.Cleanup(captured.Release)
	return captured
}

// TestCompletionsFullGetCompletionsMarshalsItems is a dispatch test: the
// provider capability sent to the server must, when its GetCompletions
// method is invoked, call into the plugin's GetCompletions callback with the
// right (bufID, line, col) and marshal every field of the returned items
// (including a TextEdit) back out correctly.
func TestCompletionsFullGetCompletionsMarshalsItems(t *testing.T) {
	var gotBufID, gotLine, gotCol uint32
	provider := captureCompletionProvider(t, CompletionHandlers{
		GetCompletions: func(bufID, line, col uint32) []CompletionItem {
			gotBufID, gotLine, gotCol = bufID, line, col
			return []CompletionItem{{
				Label:      "1.2.3",
				Kind:       12,
				Detail:     "latest",
				InsertText: "1.2.3",
				SortText:   "a",
				FilterText: "1.2.3",
				Data:       "npm:leftpad",
				TextEdit: &TextEdit{
					From:    Position{Line: 4, Col: 1},
					To:      Position{Line: 4, Col: 6},
					NewText: "\"1.2.3\"",
				},
			}}
		},
	})

	fut, rel := provider.GetCompletions(context.Background(), func(p pluginproto.CompletionProvider_getCompletions_Params) error {
		p.SetBufId(7)
		p.SetLine(4)
		p.SetCol(3)
		return nil
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		t.Fatalf("GetCompletions: %v", err)
	}

	if gotBufID != 7 || gotLine != 4 || gotCol != 3 {
		t.Errorf("callback args = (%d,%d,%d), want (7,4,3)", gotBufID, gotLine, gotCol)
	}

	items, err := res.Items()
	if err != nil {
		t.Fatalf("Items: %v", err)
	}
	if items.Len() != 1 {
		t.Fatalf("Items.Len() = %d, want 1", items.Len())
	}
	it := items.At(0)
	label, _ := it.Label()
	detail, _ := it.Detail()
	insert, _ := it.InsertText()
	sortText, _ := it.SortText()
	filterText, _ := it.FilterText()
	data, _ := it.Data()
	if label != "1.2.3" || it.Kind() != 12 || detail != "latest" || insert != "1.2.3" ||
		sortText != "a" || filterText != "1.2.3" || data != "npm:leftpad" {
		t.Errorf("item = %+v (label=%q detail=%q insert=%q sort=%q filter=%q data=%q), unexpected field(s)",
			it, label, detail, insert, sortText, filterText, data)
	}
	if !it.HasTextEdit() {
		t.Fatal("HasTextEdit() = false, want true")
	}
	te, err := it.TextEdit()
	if err != nil {
		t.Fatalf("TextEdit: %v", err)
	}
	from, _ := te.From()
	to, _ := te.To()
	newText, _ := te.NewText()
	if from.Line() != 4 || from.Col() != 1 || to.Line() != 4 || to.Col() != 6 || newText != "\"1.2.3\"" {
		t.Errorf("textEdit = {from:%d,%d to:%d,%d newText:%q}, want {4,1 4,6 %q}",
			from.Line(), from.Col(), to.Line(), to.Col(), newText, "\"1.2.3\"")
	}
}

// TestCompletionsFullResolveCompletionInvokesCallback verifies the deferred
// resolve path: the provider's ResolveCompletion method must decode the
// inbound item, pass it to the plugin's ResolveCompletion callback, and
// marshal whatever the callback returns (e.g. a Detail filled in from a slow
// lookup deferred out of GetCompletions) back to the caller.
func TestCompletionsFullResolveCompletionInvokesCallback(t *testing.T) {
	var gotData string
	provider := captureCompletionProvider(t, CompletionHandlers{
		GetCompletions: func(bufID, line, col uint32) []CompletionItem { return nil },
		ResolveCompletion: func(item CompletionItem) CompletionItem {
			gotData = item.Data
			item.Detail = "resolved:" + item.Data
			return item
		},
	})

	fut, rel := provider.ResolveCompletion(context.Background(), func(p pluginproto.CompletionProvider_resolveCompletion_Params) error {
		ci, err := p.NewItem()
		if err != nil {
			return err
		}
		if err := ci.SetLabel("1.2.3"); err != nil {
			return err
		}
		return ci.SetData("npm:leftpad")
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		t.Fatalf("ResolveCompletion: %v", err)
	}
	if gotData != "npm:leftpad" {
		t.Errorf("callback item.Data = %q, want %q", gotData, "npm:leftpad")
	}
	out, err := res.Item()
	if err != nil {
		t.Fatalf("Item: %v", err)
	}
	detail, _ := out.Detail()
	if detail != "resolved:npm:leftpad" {
		t.Errorf("resolved.Detail = %q, want %q", detail, "resolved:npm:leftpad")
	}
}

// TestCompletionsRegistersSimpleProvider verifies the Completions shorthand
// (no resolve step) wires GetCompletions through without requiring
// ResolveCompletion to be set.
func TestCompletionsRegistersSimpleProvider(t *testing.T) {
	var registered bool
	fake := &fakeFullEditorApi{registerCompletionProvider: func(call pluginproto.EditorApi_registerCompletionProvider) error {
		registered = true
		_, err := call.AllocResults()
		return err
	}}
	api := newTestApi(t, fake)
	if err := api.Completions(func(bufID, line, col uint32) []CompletionItem { return nil }); err != nil {
		t.Fatalf("Completions: %v", err)
	}
	if !registered {
		t.Fatal("RegisterCompletionProvider was never called")
	}
}
