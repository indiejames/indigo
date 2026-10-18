package sdk

import (
	"testing"

	"github.com/indiejames/indigo/sdk/pluginproto"
)

// TestApplyEditAtSendsItsBaseVersion: the version is what lets the editor
// rebase the edit past typing that landed while the plugin worked, the
// generation is what tells it the buffer was not replaced in between, and the
// flag is what tells a real version 0 from "none given". ApplyEdit must send
// no version, so an older call keeps its old meaning.
func TestApplyEditAtSendsItsBaseVersion(t *testing.T) {
	type sent struct {
		version, generation uint64
		has                 bool
	}
	var got []sent
	fake := &fakeFullEditorApi{applyEdit: func(call pluginproto.EditorApi_applyEdit) error {
		got = append(got, sent{call.Args().BaseVersion(), call.Args().BaseGeneration(), call.Args().HasBaseVersion()})
		_, err := call.AllocResults()
		return err
	}}
	api := newTestApi(t, fake)
	if err := api.ApplyEditAt(1, BufferVersion{}, []TextEdit{{NewText: "x"}}); err != nil {
		t.Fatal(err)
	}
	if err := api.ApplyEditAt(1, BufferVersion{version: 12, generation: 3}, []TextEdit{{NewText: "x"}}); err != nil {
		t.Fatal(err)
	}
	if err := api.ApplyEdit(1, []TextEdit{{NewText: "x"}}); err != nil {
		t.Fatal(err)
	}
	want := []sent{{0, 0, true}, {12, 3, true}, {0, 0, false}}
	if len(got) != len(want) {
		t.Fatalf("calls = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("call %d sent %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestReadBufferVersionedReturnsContentAndVersion(t *testing.T) {
	fake := &fakeFullEditorApi{readBuffer: func(call pluginproto.EditorApi_readBuffer) error {
		res, err := call.AllocResults()
		if err != nil {
			return err
		}
		res.SetVersion(9)
		res.SetGeneration(2)
		return res.SetContent("package main\n")
	}}
	api := newTestApi(t, fake)
	content, version, err := api.ReadBufferVersioned(1)
	want := BufferVersion{version: 9, generation: 2}
	if err != nil || content != "package main\n" || version != want {
		t.Fatalf("ReadBufferVersioned = %q, %+v, %v; want %q, %+v", content, version, err, "package main\n", want)
	}
}
