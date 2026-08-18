package client

import (
	"testing"

	"capnproto.org/go/capnp/v3"
	proto "github.com/indiejames/indigo/internal/proto"
)

// TestCompletionFromProtoCarriesSource is a regression test for the plugin-
// completion-provider feature: completionFromProto must decode the new
// Source field (empty = language server, non-empty = the plugin that
// supplied the item) so the accept path and a later ResolveCompletion call
// know where the item came from.
func TestCompletionFromProtoCarriesSource(t *testing.T) {
	_, seg, err := capnp.NewMessage(capnp.SingleSegment(nil))
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	it, err := proto.NewRootCompletionItem(seg)
	if err != nil {
		t.Fatalf("NewRootCompletionItem: %v", err)
	}
	if err := it.SetLabel("1.2.3"); err != nil {
		t.Fatal(err)
	}
	if err := it.SetSource("npm-versions"); err != nil {
		t.Fatal(err)
	}

	c := completionFromProto(it)
	if c.Label != "1.2.3" {
		t.Errorf("Label = %q, want %q", c.Label, "1.2.3")
	}
	if c.Source != "npm-versions" {
		t.Errorf("Source = %q, want %q", c.Source, "npm-versions")
	}
}

// TestCompletionFromProtoEmptySourceMeansLSP verifies the default/omitted
// case decodes to an empty Source, matching every existing LSP-sourced item.
func TestCompletionFromProtoEmptySourceMeansLSP(t *testing.T) {
	_, seg, err := capnp.NewMessage(capnp.SingleSegment(nil))
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	it, err := proto.NewRootCompletionItem(seg)
	if err != nil {
		t.Fatalf("NewRootCompletionItem: %v", err)
	}
	if err := it.SetLabel("Foo"); err != nil {
		t.Fatal(err)
	}

	c := completionFromProto(it)
	if c.Source != "" {
		t.Errorf("Source = %q, want empty for an LSP-sourced item", c.Source)
	}
}
