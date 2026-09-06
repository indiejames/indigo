package sdk

import (
	"context"
	"testing"

	"github.com/indiejames/indigo/internal/proto/pluginproto"
)

// TestDecorationMarshalRoundTrip is a regression test for a field that was
// added to Decoration but never wired into decorProviderServer's marshal
// loop, so it silently stayed zero on the wire.
//
// That gap was invisible to every other test: the editor-side tests build a
// decoration struct directly and never cross this boundary, so a missing
// SetX here breaks the feature end to end while everything still passes.
// Asserting the whole round trip is what closes that.
func TestDecorationMarshalRoundTrip(t *testing.T) {
	want := Decoration{
		Line:      7,
		Col:       3,
		Text:      "return err",
		Kind:      DecorationRemovedLine,
		EndCol:    9,
		TextColor: "#FF5555",
		OldLine:   42,
	}

	srv := &decorProviderServer{h: DecorationHandlers{
		GetDecorations: func(uint32, uint64, Range) []Decoration { return []Decoration{want} },
	}}

	client := pluginproto.DecorationProvider_ServerToClient(srv)
	defer client.Release()

	fut, rel := client.GetDecorations(context.Background(),
		func(p pluginproto.DecorationProvider_getDecorations_Params) error { return nil })
	defer rel()

	res, err := fut.Struct()
	if err != nil {
		t.Fatalf("GetDecorations: %v", err)
	}
	list, err := res.Decorations()
	if err != nil {
		t.Fatalf("Decorations: %v", err)
	}
	if list.Len() != 1 {
		t.Fatalf("got %d decorations, want 1", list.Len())
	}
	got := list.At(0)

	text, _ := got.Text()
	textColor, _ := got.TextColor()
	checks := []struct {
		field     string
		got, want any
	}{
		{"Line", got.Line(), want.Line},
		{"Col", got.Col(), want.Col},
		{"Text", text, want.Text},
		{"Kind", uint16(got.Kind()), uint16(want.Kind)},
		{"EndCol", got.EndCol(), want.EndCol},
		{"TextColor", textColor, want.TextColor},
		{"OldLine", got.OldLine(), want.OldLine},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v — not copied in decorProviderServer.GetDecorations",
				c.field, c.got, c.want)
		}
	}
}
