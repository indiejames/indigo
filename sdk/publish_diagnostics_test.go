package sdk

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/indiejames/indigo/internal/proto/pluginproto"
)

// capturedDiagnosticsCall is what the fake server observed a PublishDiagnostics
// call actually carried.
type capturedDiagnosticsCall struct {
	bufID   uint32
	version uint64
	diags   []Diagnostic
}

type fakePublishDiagnosticsApi struct {
	pluginproto.EditorApi_Server
	called chan capturedDiagnosticsCall
}

func (f *fakePublishDiagnosticsApi) PublishDiagnostics(_ context.Context, call pluginproto.EditorApi_publishDiagnostics) error {
	args := call.Args()
	got := capturedDiagnosticsCall{bufID: args.BufId(), version: args.Version()}
	list, err := args.Diagnostics()
	if err != nil {
		return err
	}
	for i := range list.Len() {
		item := list.At(i)
		rng, err := item.Range()
		if err != nil {
			return err
		}
		start, err := rng.Start()
		if err != nil {
			return err
		}
		end, err := rng.End()
		if err != nil {
			return err
		}
		msg, err := item.Message_()
		if err != nil {
			return err
		}
		got.diags = append(got.diags, Diagnostic{
			Range: Range{
				Start: Position{start.Line(), start.Col()},
				End:   Position{end.Line(), end.Col()},
			},
			Severity: DiagnosticSeverity(item.Severity()),
			Message:  msg,
		})
	}
	f.called <- got
	_, err = call.AllocResults()
	return err
}

func newPublishDiagnosticsTestApi(t *testing.T, fake *fakePublishDiagnosticsApi) *Api {
	t.Helper()
	client := pluginproto.EditorApi_ServerToClient(fake)
	t.Cleanup(client.Release)
	return &Api{api: client}
}

// TestPublishDiagnosticsMarshalsCorrectlyAndDoesNotBlock verifies both halves
// of the contract: the call reaches the server with bufID/version/diagnostics
// intact, and — like RefreshDecorations (see refresh_decorations_test.go) —
// the caller doesn't block on the RPC round trip.
func TestPublishDiagnosticsMarshalsCorrectlyAndDoesNotBlock(t *testing.T) {
	fake := &fakePublishDiagnosticsApi{called: make(chan capturedDiagnosticsCall, 1)}
	api := newPublishDiagnosticsTestApi(t, fake)

	diags := []Diagnostic{{
		Range:    Range{Start: Position{Line: 1, Col: 2}, End: Position{Line: 1, Col: 8}},
		Severity: SeverityWarning,
		Message:  "possible misspelling",
	}}

	start := time.Now()
	api.PublishDiagnostics(7, 42, diags)
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("PublishDiagnostics took %v — it should return immediately (fire-and-forget)", elapsed)
	}

	select {
	case got := <-fake.called:
		if got.bufID != 7 {
			t.Errorf("bufID = %d, want 7", got.bufID)
		}
		if got.version != 42 {
			t.Errorf("version = %d, want 42", got.version)
		}
		if len(got.diags) != 1 || got.diags[0] != diags[0] {
			t.Errorf("diags = %+v, want %+v", got.diags, diags)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("PublishDiagnostics never reached the server")
	}
}

// TestPublishDiagnosticsEmptyClears verifies an empty/nil diags slice still
// marshals (and dispatches) correctly — this is the "clear my diagnostics
// for this buffer" call plugins make on becoming clean.
func TestPublishDiagnosticsEmptyClears(t *testing.T) {
	fake := &fakePublishDiagnosticsApi{called: make(chan capturedDiagnosticsCall, 1)}
	api := newPublishDiagnosticsTestApi(t, fake)

	api.PublishDiagnostics(7, 42, nil)

	select {
	case got := <-fake.called:
		if len(got.diags) != 0 {
			t.Errorf("diags = %+v, want empty", got.diags)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("PublishDiagnostics never reached the server")
	}
}
