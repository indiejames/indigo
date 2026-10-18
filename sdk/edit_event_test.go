package sdk

import (
	"context"
	"testing"

	"github.com/indiejames/indigo/sdk/pluginproto"
)

// editHandlerEditorApi captures the handler a plugin registers for edit events.
type editHandlerEditorApi struct {
	pluginproto.EditorApi_Server
	handler pluginproto.EditEventHandler
}

func (f *editHandlerEditorApi) RegisterEditHandler(_ context.Context, call pluginproto.EditorApi_registerEditHandler) error {
	f.handler = call.Args().Handler().AddRef()
	return nil
}

// fire sends one linesChanged as an editor would; withRange false is an editor
// from before the range fields.
func (f *editHandlerEditorApi) fire(t *testing.T, withRange bool) {
	t.Helper()
	fut, rel := f.handler.LinesChanged(context.Background(), func(p pluginproto.EditEventHandler_linesChanged_Params) error {
		p.SetBufId(4)
		if err := p.SetFilePath("/a.go"); err != nil {
			return err
		}
		p.SetAtLine(2)
		p.SetLineDelta(-1)
		if withRange {
			p.SetFromCol(7)
			p.SetToLine(3)
			p.SetToCol(0)
			p.SetHasRange(true)
		}
		return nil
	})
	defer rel()
	if _, err := fut.Struct(); err != nil {
		t.Fatalf("linesChanged: %v", err)
	}
}

func TestOnEditReceivesTheEditsRange(t *testing.T) {
	fake := &editHandlerEditorApi{}
	client := pluginproto.EditorApi_ServerToClient(fake)
	t.Cleanup(client.Release)
	api := &Api{api: client}

	var got []EditEvent
	if err := api.OnEdit(func(e EditEvent) { got = append(got, e) }); err != nil {
		t.Fatalf("OnEdit: %v", err)
	}
	t.Cleanup(fake.handler.Release)
	fake.fire(t, true)
	fake.fire(t, false)

	want := []EditEvent{
		{BufID: 4, FilePath: "/a.go", FromLine: 2, FromCol: 7, ToLine: 3, ToCol: 0, LineDelta: -1, HasRange: true},
		// An older editor's event: line and count only, and no range claimed.
		{BufID: 4, FilePath: "/a.go", FromLine: 2, LineDelta: -1},
	}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("events = %+v, want %+v", got, want)
	}
}

// TestOnEditEventStillReportsLineAndDelta keeps the older, line-only
// registration working for plugins written against it.
func TestOnEditEventStillReportsLineAndDelta(t *testing.T) {
	fake := &editHandlerEditorApi{}
	client := pluginproto.EditorApi_ServerToClient(fake)
	t.Cleanup(client.Release)
	api := &Api{api: client}

	type call struct {
		bufID     uint32
		path      string
		atLine    uint32
		lineDelta int32
	}
	var got []call
	if err := api.OnEditEvent(func(bufID uint32, path string, atLine uint32, lineDelta int32) {
		got = append(got, call{bufID, path, atLine, lineDelta})
	}); err != nil {
		t.Fatalf("OnEditEvent: %v", err)
	}
	t.Cleanup(fake.handler.Release)
	fake.fire(t, true)
	if want := (call{4, "/a.go", 2, -1}); len(got) != 1 || got[0] != want {
		t.Fatalf("calls = %+v, want %+v", got, want)
	}
}
