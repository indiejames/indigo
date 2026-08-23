package sdk

import (
	"context"
	"testing"
	"time"

	"github.com/indiejames/indigo/internal/proto/pluginproto"
)

// fakePublishWorkspaceDiagnosticsApi mirrors fakeEditorApi
// (refresh_decorations_test.go): only PublishWorkspaceDiagnostics is
// implemented, and the handler blocks on release to prove the call doesn't
// block its caller.
type fakePublishWorkspaceDiagnosticsApi struct {
	pluginproto.EditorApi_Server
	called   chan struct{}
	release  chan struct{}
	gotPath  string
	gotDiags []Diagnostic
}

func (f *fakePublishWorkspaceDiagnosticsApi) PublishWorkspaceDiagnostics(_ context.Context, call pluginproto.EditorApi_publishWorkspaceDiagnostics) error {
	args := call.Args()
	f.gotPath, _ = args.Path()
	rawList, _ := args.Diagnostics()
	for i := range rawList.Len() {
		item := rawList.At(i)
		msg, _ := item.Message_()
		rng, _ := item.Range()
		start, _ := rng.Start()
		end, _ := rng.End()
		f.gotDiags = append(f.gotDiags, Diagnostic{
			Range: Range{
				Start: Position{Line: start.Line(), Col: start.Col()},
				End:   Position{Line: end.Line(), Col: end.Col()},
			},
			Severity: DiagnosticSeverity(item.Severity()),
			Message:  msg,
		})
	}
	close(f.called)
	<-f.release
	_, err := call.AllocResults()
	return err
}

// TestPublishWorkspaceDiagnosticsDoesNotBlockCaller verifies the
// fire-and-forget contract (matching PublishDiagnostics/RefreshDecorations)
// and that path + diagnostic fields round-trip correctly.
func TestPublishWorkspaceDiagnosticsDoesNotBlockCaller(t *testing.T) {
	fake := &fakePublishWorkspaceDiagnosticsApi{called: make(chan struct{}), release: make(chan struct{})}
	defer close(fake.release)

	client := pluginproto.EditorApi_ServerToClient(fake)
	defer client.Release()
	api := &Api{api: client}

	diags := []Diagnostic{{
		Range:    Range{Start: Position{Line: 3, Col: 1}, End: Position{Line: 3, Col: 8}},
		Severity: SeverityInfo,
		Message:  "possible misspelling",
	}}

	start := time.Now()
	api.PublishWorkspaceDiagnostics("/repo/README.md", diags)
	elapsed := time.Since(start)
	if elapsed > 100*time.Millisecond {
		t.Fatalf("PublishWorkspaceDiagnostics took %v — it should return immediately without waiting for the server", elapsed)
	}

	select {
	case <-fake.called:
	case <-time.After(2 * time.Second):
		t.Fatal("PublishWorkspaceDiagnostics never reached the server")
	}

	if fake.gotPath != "/repo/README.md" {
		t.Errorf("gotPath = %q, want /repo/README.md", fake.gotPath)
	}
	if len(fake.gotDiags) != 1 || fake.gotDiags[0].Message != "possible misspelling" || fake.gotDiags[0].Severity != SeverityInfo {
		t.Errorf("gotDiags = %+v, want one SeverityInfo diagnostic with message %q", fake.gotDiags, "possible misspelling")
	}
	if fake.gotDiags[0].Range.Start != (Position{Line: 3, Col: 1}) || fake.gotDiags[0].Range.End != (Position{Line: 3, Col: 8}) {
		t.Errorf("gotDiags[0].Range = %+v, want Start={3,1} End={3,8}", fake.gotDiags[0].Range)
	}
}

// fakeRegisterWorkspaceScanApi captures the handler passed to
// RegisterWorkspaceScanHandler so the test can invoke it directly,
// simulating the server dispatching a scan.
type fakeRegisterWorkspaceScanApi struct {
	pluginproto.EditorApi_Server
	handler pluginproto.WorkspaceScanHandler
}

func (f *fakeRegisterWorkspaceScanApi) RegisterWorkspaceScanHandler(_ context.Context, call pluginproto.EditorApi_registerWorkspaceScanHandler) error {
	f.handler = call.Args().Handler().AddRef()
	_, err := call.AllocResults()
	return err
}

// TestOnWorkspaceScanRegistersAndInvokesHandler verifies OnWorkspaceScan
// registers a handler the server can later call, and that calling it
// invokes the Go function passed by the plugin.
func TestOnWorkspaceScanRegistersAndInvokesHandler(t *testing.T) {
	fake := &fakeRegisterWorkspaceScanApi{}
	client := pluginproto.EditorApi_ServerToClient(fake)
	defer client.Release()
	api := &Api{api: client}

	called := make(chan struct{})
	if err := api.OnWorkspaceScan(func() { close(called) }); err != nil {
		t.Fatalf("OnWorkspaceScan: %v", err)
	}
	if !fake.handler.IsValid() {
		t.Fatal("RegisterWorkspaceScanHandler never received a valid handler")
	}

	fut, rel := fake.handler.Scan(context.Background(), func(pluginproto.WorkspaceScanHandler_scan_Params) error { return nil })
	defer rel()
	if _, err := fut.Struct(); err != nil {
		t.Fatalf("Scan: %v", err)
	}

	select {
	case <-called:
	case <-time.After(2 * time.Second):
		t.Fatal("registered handler's function was never invoked")
	}
}
