package client

import (
	"context"

	proto "github.com/indiejames/indigo/internal/proto"
)

// ClientWorkspaceDiag is one diagnostic from GetWorkspaceDiagnostics, the
// same shape as ClientDiag plus the path it belongs to (GetDiagnostics omits
// path since the caller already knows it — it's scoped to one bufID).
type ClientWorkspaceDiag struct {
	Path                       string
	Line, Col, EndLine, EndCol int
	Severity                   uint8
	Message, Source            string
}

// WorkspaceDiagnosticsResult bundles the aggregated diagnostics with whether
// the result was capped (see maxWorkspaceDiagnostics in internal/server).
type WorkspaceDiagnosticsResult struct {
	Items     []ClientWorkspaceDiag
	Truncated bool
}

// GetWorkspaceDiagnostics fetches diagnostics across the whole project:
// live diagnostics for every open buffer, plus whatever the last workspace
// lint scan found for files that aren't open in any buffer — see
// RescanWorkspaceDiagnostics to trigger a fresh scan. LSP/plugin diagnostic
// coverage for unopened files is a known, documented gap, not yet
// implemented.
func (r *RPC) GetWorkspaceDiagnostics(ctx context.Context) (WorkspaceDiagnosticsResult, error) {
	fut, rel := r.svc.GetWorkspaceDiagnostics(ctx, func(proto.EditorService_getWorkspaceDiagnostics_Params) error {
		return nil
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		return WorkspaceDiagnosticsResult{}, err
	}
	list, err := res.Items()
	if err != nil {
		return WorkspaceDiagnosticsResult{}, err
	}
	out := make([]ClientWorkspaceDiag, list.Len())
	for i := range out {
		it := list.At(i)
		path, _ := it.Path()
		msg, _ := it.Message_()
		src, _ := it.Source()
		out[i] = ClientWorkspaceDiag{
			Path: path,
			Line: int(it.Line()), Col: int(it.Col()),
			EndLine: int(it.EndLine()), EndCol: int(it.EndCol()),
			Severity: it.Severity(),
			Message:  msg,
			Source:   src,
		}
	}
	return WorkspaceDiagnosticsResult{Items: out, Truncated: res.Truncated()}, nil
}

// WorkspaceDiagnosticsSummary is the cheap counts-only counterpart to
// WorkspaceDiagnosticsResult.
type WorkspaceDiagnosticsSummary struct {
	ErrorCount, WarningCount, InfoCount, FileCount int
}

// GetWorkspaceDiagnosticsSummary fetches just the counts, for a
// workspace-wide status indicator that doesn't need the full item list.
func (r *RPC) GetWorkspaceDiagnosticsSummary(ctx context.Context) (WorkspaceDiagnosticsSummary, error) {
	fut, rel := r.svc.GetWorkspaceDiagnosticsSummary(ctx, func(proto.EditorService_getWorkspaceDiagnosticsSummary_Params) error {
		return nil
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		return WorkspaceDiagnosticsSummary{}, err
	}
	return WorkspaceDiagnosticsSummary{
		ErrorCount:   int(res.ErrorCount()),
		WarningCount: int(res.WarningCount()),
		InfoCount:    int(res.InfoCount()),
		FileCount:    int(res.FileCount()),
	}, nil
}

// RescanWorkspaceDiagnostics triggers an async whole-project lint scan on
// the server (see lint.Manager.ScanWorkspace) to refresh diagnostics for
// files that aren't open in any buffer. Fire-and-forget: this call returns
// once the scan has been (re)started, not once it completes — call
// GetWorkspaceDiagnostics/Summary again afterward to pick up fresh results.
func (r *RPC) RescanWorkspaceDiagnostics(ctx context.Context) error {
	fut, rel := r.svc.RescanWorkspaceDiagnostics(ctx, func(proto.EditorService_rescanWorkspaceDiagnostics_Params) error {
		return nil
	})
	defer rel()
	_, err := fut.Struct()
	return err
}
