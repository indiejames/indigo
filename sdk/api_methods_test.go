package sdk

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/indiejames/indigo/internal/proto/pluginproto"
)

// fakeFullEditorApi extends the RefreshDecorations-only fake with the
// additional EditorApi methods exercised by this file. Each field is a
// closure so individual tests can wire up only the behavior they need.
type fakeFullEditorApi struct {
	pluginproto.EditorApi_Server

	applyEdit                  func(call pluginproto.EditorApi_applyEdit) error
	readBuffer                 func(call pluginproto.EditorApi_readBuffer) error
	readRange                  func(call pluginproto.EditorApi_readRange) error
	wordAt                     func(call pluginproto.EditorApi_wordAt) error
	showPopup                  func(call pluginproto.EditorApi_showPopup) error
	showInputPrompt            func(call pluginproto.EditorApi_showInputPrompt) error
	registerCompletionProvider func(call pluginproto.EditorApi_registerCompletionProvider) error
}

func (f *fakeFullEditorApi) ApplyEdit(_ context.Context, call pluginproto.EditorApi_applyEdit) error {
	return f.applyEdit(call)
}

func (f *fakeFullEditorApi) ReadBuffer(_ context.Context, call pluginproto.EditorApi_readBuffer) error {
	return f.readBuffer(call)
}

func (f *fakeFullEditorApi) ReadRange(_ context.Context, call pluginproto.EditorApi_readRange) error {
	return f.readRange(call)
}

func (f *fakeFullEditorApi) WordAt(_ context.Context, call pluginproto.EditorApi_wordAt) error {
	return f.wordAt(call)
}

func (f *fakeFullEditorApi) ShowPopup(_ context.Context, call pluginproto.EditorApi_showPopup) error {
	return f.showPopup(call)
}

func (f *fakeFullEditorApi) ShowInputPrompt(_ context.Context, call pluginproto.EditorApi_showInputPrompt) error {
	return f.showInputPrompt(call)
}

func (f *fakeFullEditorApi) RegisterCompletionProvider(_ context.Context, call pluginproto.EditorApi_registerCompletionProvider) error {
	return f.registerCompletionProvider(call)
}

func newTestApi(t *testing.T, fake *fakeFullEditorApi) *Api {
	t.Helper()
	client := pluginproto.EditorApi_ServerToClient(fake)
	t.Cleanup(client.Release)
	return &Api{api: client}
}

// TestApplyEditMarshalsEditsCorrectly verifies bufID and every TextEdit
// field round-trip through the capnp params as expected.
func TestApplyEditMarshalsEditsCorrectly(t *testing.T) {
	var gotBufID uint32
	var gotFrom, gotTo Position
	var gotText string

	// The fake's callback runs on a capnp RPC-dispatch goroutine, not the
	// test goroutine — t.Fatal there would violate testing.T's "FailNow
	// must be called from the test goroutine" contract. Return errors
	// instead; they surface through api.ApplyEdit's own returned err below.
	fake := &fakeFullEditorApi{applyEdit: func(call pluginproto.EditorApi_applyEdit) error {
		args := call.Args()
		gotBufID = args.BufId()
		edits, err := args.Edits()
		if err != nil {
			return err
		}
		if edits.Len() != 1 {
			return fmt.Errorf("edits.Len() = %d, want 1", edits.Len())
		}
		item := edits.At(0)
		from, _ := item.From()
		to, _ := item.To()
		gotFrom = Position{from.Line(), from.Col()}
		gotTo = Position{to.Line(), to.Col()}
		gotText, _ = item.NewText()
		_, err = call.AllocResults()
		return err
	}}
	api := newTestApi(t, fake)

	err := api.ApplyEdit(7, []TextEdit{{
		From:    Position{Line: 1, Col: 2},
		To:      Position{Line: 1, Col: 5},
		NewText: "hello",
	}})
	if err != nil {
		t.Fatalf("ApplyEdit: %v", err)
	}
	if gotBufID != 7 {
		t.Errorf("bufID = %d, want 7", gotBufID)
	}
	if gotFrom != (Position{1, 2}) || gotTo != (Position{1, 5}) {
		t.Errorf("from/to = %v/%v, want {1,2}/{1,5}", gotFrom, gotTo)
	}
	if gotText != "hello" {
		t.Errorf("NewText = %q, want %q", gotText, "hello")
	}
}

// TestApplyEditPropagatesServerError verifies a server-side rejection (e.g.
// an out-of-range edit) surfaces to the caller rather than being swallowed.
func TestApplyEditPropagatesServerError(t *testing.T) {
	fake := &fakeFullEditorApi{applyEdit: func(call pluginproto.EditorApi_applyEdit) error {
		return errors.New("edit out of range")
	}}
	api := newTestApi(t, fake)

	err := api.ApplyEdit(1, []TextEdit{{NewText: "x"}})
	if err == nil {
		t.Fatal("expected the server error to propagate, got nil")
	}
}

// TestReadBufferReturnsContent verifies the returned content round-trips.
func TestReadBufferReturnsContent(t *testing.T) {
	fake := &fakeFullEditorApi{readBuffer: func(call pluginproto.EditorApi_readBuffer) error {
		res, err := call.AllocResults()
		if err != nil {
			return err
		}
		return res.SetContent("package main\n")
	}}
	api := newTestApi(t, fake)

	got, err := api.ReadBuffer(1)
	if err != nil {
		t.Fatalf("ReadBuffer: %v", err)
	}
	if got != "package main\n" {
		t.Errorf("ReadBuffer = %q, want %q", got, "package main\n")
	}
}

// TestReadBufferPropagatesError verifies a lookup failure (e.g. unknown
// bufID) surfaces as an error rather than an empty string.
func TestReadBufferPropagatesError(t *testing.T) {
	fake := &fakeFullEditorApi{readBuffer: func(call pluginproto.EditorApi_readBuffer) error {
		return errors.New("unknown buffer")
	}}
	api := newTestApi(t, fake)

	if _, err := api.ReadBuffer(999); err == nil {
		t.Fatal("expected an error for an unknown buffer, got nil")
	}
}

// TestReadRangeMarshalsPositions verifies from/to positions round-trip and
// the returned text is passed through.
func TestReadRangeMarshalsPositions(t *testing.T) {
	var gotFrom, gotTo Position
	fake := &fakeFullEditorApi{readRange: func(call pluginproto.EditorApi_readRange) error {
		args := call.Args()
		f, _ := args.From()
		tt, _ := args.To()
		gotFrom = Position{f.Line(), f.Col()}
		gotTo = Position{tt.Line(), tt.Col()}
		res, err := call.AllocResults()
		if err != nil {
			return err
		}
		return res.SetText("selected text")
	}}
	api := newTestApi(t, fake)

	got, err := api.ReadRange(1, Position{Line: 0, Col: 1}, Position{Line: 2, Col: 3})
	if err != nil {
		t.Fatalf("ReadRange: %v", err)
	}
	if gotFrom != (Position{0, 1}) || gotTo != (Position{2, 3}) {
		t.Errorf("from/to = %v/%v, want {0,1}/{2,3}", gotFrom, gotTo)
	}
	if got != "selected text" {
		t.Errorf("ReadRange = %q, want %q", got, "selected text")
	}
}

// TestWordAtNotFoundReturnsZeroPositions verifies the documented contract:
// when the server reports Found=false, WordAt returns found=false and
// zero-value positions rather than whatever garbage Start/End happen to
// hold in the result struct.
func TestWordAtNotFoundReturnsZeroPositions(t *testing.T) {
	fake := &fakeFullEditorApi{wordAt: func(call pluginproto.EditorApi_wordAt) error {
		res, err := call.AllocResults()
		if err != nil {
			return err
		}
		res.SetFound(false)
		return nil
	}}
	api := newTestApi(t, fake)

	start, end, found, err := api.WordAt(1, Position{Line: 0, Col: 0})
	if err != nil {
		t.Fatalf("WordAt: %v", err)
	}
	if found {
		t.Error("found = true, want false")
	}
	if start != (Position{}) || end != (Position{}) {
		t.Errorf("start/end = %v/%v, want zero values when not found", start, end)
	}
}

// TestWordAtFoundReturnsBoundaries verifies a found word's start/end
// positions round-trip correctly.
func TestWordAtFoundReturnsBoundaries(t *testing.T) {
	fake := &fakeFullEditorApi{wordAt: func(call pluginproto.EditorApi_wordAt) error {
		res, err := call.AllocResults()
		if err != nil {
			return err
		}
		res.SetFound(true)
		s, err := res.NewStart()
		if err != nil {
			return err
		}
		s.SetLine(3)
		s.SetCol(1)
		e, err := res.NewEnd()
		if err != nil {
			return err
		}
		e.SetLine(3)
		e.SetCol(5)
		return nil
	}}
	api := newTestApi(t, fake)

	start, end, found, err := api.WordAt(1, Position{Line: 3, Col: 2})
	if err != nil {
		t.Fatalf("WordAt: %v", err)
	}
	if !found {
		t.Fatal("found = false, want true")
	}
	if start != (Position{3, 1}) || end != (Position{3, 5}) {
		t.Errorf("start/end = %v/%v, want {3,1}/{3,5}", start, end)
	}
}

// TestShowPopupSelectedInvokesOnSelectWithData is a dispatch test: the
// handler capability sent to the server must, when its Selected method is
// invoked (simulating the user picking an item), call back into the
// plugin's onSelect with the chosen item's Data.
func TestShowPopupSelectedInvokesOnSelectWithData(t *testing.T) {
	var capturedHandler pluginproto.PopupHandler
	fake := &fakeFullEditorApi{showPopup: func(call pluginproto.EditorApi_showPopup) error {
		capturedHandler = call.Args().Handler().AddRef()
		_, err := call.AllocResults()
		return err
	}}
	api := newTestApi(t, fake)

	var gotData string
	selectCalled := false
	err := api.ShowPopup("title", []PopupItem{{Label: "A", Data: "payload"}}, func(data string) {
		selectCalled = true
		gotData = data
	}, nil)
	if err != nil {
		t.Fatalf("ShowPopup: %v", err)
	}
	defer capturedHandler.Release()

	fut, rel := capturedHandler.Selected(context.Background(), func(p pluginproto.PopupHandler_selected_Params) error {
		return p.SetData("payload")
	})
	defer rel()
	if _, err := fut.Struct(); err != nil {
		t.Fatalf("Selected: %v", err)
	}

	if !selectCalled {
		t.Fatal("onSelect was never invoked")
	}
	if gotData != "payload" {
		t.Errorf("onSelect data = %q, want %q", gotData, "payload")
	}
}

// TestShowPopupCancelledInvokesOnCancel mirrors the Selected test for the
// dismiss (Esc) path.
func TestShowPopupCancelledInvokesOnCancel(t *testing.T) {
	var capturedHandler pluginproto.PopupHandler
	fake := &fakeFullEditorApi{showPopup: func(call pluginproto.EditorApi_showPopup) error {
		capturedHandler = call.Args().Handler().AddRef()
		_, err := call.AllocResults()
		return err
	}}
	api := newTestApi(t, fake)

	cancelCalled := false
	err := api.ShowPopup("title", nil, nil, func() { cancelCalled = true })
	if err != nil {
		t.Fatalf("ShowPopup: %v", err)
	}
	defer capturedHandler.Release()

	fut, rel := capturedHandler.Cancelled(context.Background(), nil)
	defer rel()
	if _, err := fut.Struct(); err != nil {
		t.Fatalf("Cancelled: %v", err)
	}
	if !cancelCalled {
		t.Fatal("onCancel was never invoked")
	}
}

// TestShowInputPromptConfirmedInvokesOnConfirmWithText mirrors the popup
// dispatch tests for the input-prompt handler's Confirmed path.
func TestShowInputPromptConfirmedInvokesOnConfirmWithText(t *testing.T) {
	var capturedHandler pluginproto.InputPromptHandler
	fake := &fakeFullEditorApi{showInputPrompt: func(call pluginproto.EditorApi_showInputPrompt) error {
		capturedHandler = call.Args().Handler().AddRef()
		_, err := call.AllocResults()
		return err
	}}
	api := newTestApi(t, fake)

	var gotText string
	err := api.ShowInputPrompt("title", "placeholder", func(text string) {
		gotText = text
	}, nil)
	if err != nil {
		t.Fatalf("ShowInputPrompt: %v", err)
	}
	defer capturedHandler.Release()

	fut, rel := capturedHandler.Confirmed(context.Background(), func(p pluginproto.InputPromptHandler_confirmed_Params) error {
		return p.SetText("typed value")
	})
	defer rel()
	if _, err := fut.Struct(); err != nil {
		t.Fatalf("Confirmed: %v", err)
	}
	if gotText != "typed value" {
		t.Errorf("onConfirm text = %q, want %q", gotText, "typed value")
	}
}

// TestShowInputPromptCancelledInvokesOnCancel mirrors the popup Cancelled
// dispatch test for the input-prompt handler.
func TestShowInputPromptCancelledInvokesOnCancel(t *testing.T) {
	var capturedHandler pluginproto.InputPromptHandler
	fake := &fakeFullEditorApi{showInputPrompt: func(call pluginproto.EditorApi_showInputPrompt) error {
		capturedHandler = call.Args().Handler().AddRef()
		_, err := call.AllocResults()
		return err
	}}
	api := newTestApi(t, fake)

	cancelCalled := false
	err := api.ShowInputPrompt("title", "placeholder", nil, func() { cancelCalled = true })
	if err != nil {
		t.Fatalf("ShowInputPrompt: %v", err)
	}
	defer capturedHandler.Release()

	fut, rel := capturedHandler.Cancelled(context.Background(), nil)
	defer rel()
	if _, err := fut.Struct(); err != nil {
		t.Fatalf("Cancelled: %v", err)
	}
	if !cancelCalled {
		t.Fatal("onCancel was never invoked")
	}
}
