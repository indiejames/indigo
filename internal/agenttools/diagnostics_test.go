package agenttools

import (
	"context"
	"encoding/json"
	"github.com/indiejames/indigo/internal/syncevent"
	"strings"
	"testing"
	"time"

	"github.com/indiejames/indigo/internal/client"
	"github.com/indiejames/indigo/internal/debuglog"
)

func TestExecGetLogsFiltersAndReportsTruncation(t *testing.T) {
	t.Setenv("INDIGO_LOG_DIR", t.TempDir())
	debuglog.Write("client", "ApplyOp FAILED buf=3")
	debuglog.Write("server", "ApplyOp REJECTED buf=3")
	debuglog.Write("app", "unrelated")

	out, isErr := execGetLogs(getLogsInput{})
	if isErr {
		t.Fatalf("unexpected error result: %s", out)
	}
	for _, want := range []string{"FAILED", "REJECTED", "unrelated"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}

	out, _ = execGetLogs(getLogsInput{Tag: "server"})
	if strings.Contains(out, "FAILED") || !strings.Contains(out, "REJECTED") {
		t.Errorf("tag filter wrong:\n%s", out)
	}

	out, _ = execGetLogs(getLogsInput{Contains: "buf=3"})
	if strings.Contains(out, "unrelated") {
		t.Errorf("substring filter let an unrelated line through:\n%s", out)
	}

	// Truncation must be announced — silently dropping lines is how someone
	// concludes an event never happened when it just fell off the top.
	out, _ = execGetLogs(getLogsInput{MaxLines: 1})
	if !strings.Contains(out, "truncated") {
		t.Errorf("truncated output didn't say so:\n%s", out)
	}
}

func TestExecGetLogsRejectsBadSince(t *testing.T) {
	t.Setenv("INDIGO_LOG_DIR", t.TempDir())
	out, isErr := execGetLogs(getLogsInput{Since: "yesterday"})
	if !isErr || !strings.Contains(out, "bad since") {
		t.Errorf("got (%q, %v), want an error mentioning bad since", out, isErr)
	}
	if out, isErr := execGetLogs(getLogsInput{Since: "-5m"}); !isErr {
		t.Errorf("negative since accepted: %q", out)
	}
}

func TestExecGetLogsExcludesOlderThanSince(t *testing.T) {
	t.Setenv("INDIGO_LOG_DIR", t.TempDir())
	debuglog.Write("server", "recent line")

	// A window ending before the write happened must exclude it. Expressed as
	// a tiny window rather than by faking clocks: the line was written just
	// now, so anything shorter than the test's own runtime excludes it only if
	// filtering works at all — so use a window that definitely predates it.
	time.Sleep(5 * time.Millisecond)
	out, _ := execGetLogs(getLogsInput{Since: "1ms"})
	if strings.Contains(out, "recent line") {
		t.Errorf("since=1ms returned a line written earlier:\n%s", out)
	}
}

func TestFormatSyncStateFlagsLaggingClient(t *testing.T) {
	states := []client.BufferSyncState{{
		BufID: 1, Path: "/tmp/a.go", Version: 10, Generation: 2,
		Dirty: true, ContentSha256: []byte{0xde, 0xad, 0xbe, 0xef}, ContentBytes: 42,
		LineCount: 3, HistoryLen: 10,
		Clients: []client.ClientSyncState{
			{ClientID: 1, AckedVersion: 10, ConnID: 11},
			{ClientID: 2, AckedVersion: 4, ConnID: 22},
		},
	}}
	out := formatSyncState(states)

	if !strings.Contains(out, "version=10 generation=2 dirty=true") {
		t.Errorf("missing buffer summary:\n%s", out)
	}
	if !strings.Contains(out, "6 version(s) behind") {
		t.Errorf("lagging client not flagged — that is the signal this tool exists to surface:\n%s", out)
	}
	if strings.Count(out, "behind") != 1 {
		t.Errorf("caught-up client was also flagged as behind:\n%s", out)
	}
}

// TestFormatSyncStateNeverIncludesContent is the privacy guard: this output is
// what gets pasted into bug reports, so buffer text must never reach it.
func TestFormatSyncStateNeverIncludesContent(t *testing.T) {
	const secret = "SUPER_SECRET_TOKEN"
	states := []client.BufferSyncState{{
		BufID: 1, Path: "/tmp/a.go", Version: 1,
		ContentSha256: []byte{0x01, 0x02}, ContentBytes: uint64(len(secret)),
	}}
	if out := formatSyncState(states); strings.Contains(out, secret) {
		t.Fatalf("buffer content leaked into sync state output:\n%s", out)
	}
}

func TestFormatSyncStateFlagsOrphanedBuffer(t *testing.T) {
	states := []client.BufferSyncState{{BufID: 4, Path: "/tmp/a.go", Version: 2}}
	if out := formatSyncState(states); !strings.Contains(out, "orphaned") {
		t.Errorf("a buffer with no clients should be called out:\n%s", out)
	}
}

func TestBuffersWithLaggingClients(t *testing.T) {
	states := []client.BufferSyncState{
		{Version: 5, Clients: []client.ClientSyncState{{AckedVersion: 5}}},
		{Version: 5, Clients: []client.ClientSyncState{{AckedVersion: 5}, {AckedVersion: 1}}},
		{Version: 5, Clients: []client.ClientSyncState{{AckedVersion: 0}, {AckedVersion: 0}}},
	}
	// The second and third buffers lag; the count is per buffer, not per client.
	if got := buffersWithLaggingClients(states); got != 2 {
		t.Errorf("buffersWithLaggingClients = %d, want 2", got)
	}
}

// TestDiagnosticToolsAreRegisteredAndDispatched guards the gap between the two
// halves of adding a tool: a definition in AllTools with no case in ExecTool is
// advertised to the model and then fails at call time, and a case with no
// definition is unreachable. Neither shows up in a unit test of the exec
// function itself.
func TestDiagnosticToolsAreRegisteredAndDispatched(t *testing.T) {
	t.Setenv("INDIGO_LOG_DIR", t.TempDir())

	for _, name := range []string{"get_logs", "get_sync_events", "get_sync_state", "check_buffer_consistency", "report_bundle"} {
		var def *ToolDef
		for i, td := range AllTools() {
			if td.Name == name {
				def = &AllTools()[i]
				break
			}
		}
		if def == nil {
			t.Errorf("%s is missing from AllTools()", name)
			continue
		}
		if def.Description == "" {
			t.Errorf("%s has no description", name)
		}
	}

	// get_logs and get_sync_events need no server, so they can be driven all
	// the way through the dispatcher — proving the name is actually wired, not
	// just defined.
	out, isErr := ExecTool(context.Background(), nil, nil, t.TempDir(), "get_logs", json.RawMessage(`{}`))
	if isErr {
		t.Errorf("ExecTool(get_logs) returned an error result: %s", out)
	}
	out, isErr = ExecTool(context.Background(), nil, nil, t.TempDir(), "get_sync_events", json.RawMessage(`{}`))
	if isErr {
		t.Errorf("ExecTool(get_sync_events) returned an error result: %s", out)
	}

	if out, isErr := ExecTool(context.Background(), nil, nil, t.TempDir(), "get_logs", json.RawMessage(`{bad`)); !isErr {
		t.Errorf("malformed input accepted: %q", out)
	}
}

func consistencySample(serverVer uint64, serverSum []byte, clients ...client.ClientBufferReport) []client.BufferConsistency {
	return []client.BufferConsistency{{
		BufID: 1, Path: "/tmp/a.go", ServerVersion: serverVer,
		ServerGeneration: 2, ServerSha256: serverSum, Clients: clients,
	}}
}

var (
	sumServer = []byte{0xaa, 0xbb}
	sumOther  = []byte{0xcc, 0xdd}
)

// TestConsistencyReportsPersistentMismatchAsDiverged is the case the tool
// exists for: nothing moved between the two samples and the hashes still
// disagree, so nothing was in flight and the two really do hold different
// content.
func TestConsistencyReportsPersistentMismatchAsDiverged(t *testing.T) {
	c := client.ClientBufferReport{ClientID: 1, Answered: true, Known: true, Version: 5, Generation: 2, ContentSha256: sumOther}
	out := formatConsistency(
		consistencySample(5, sumServer, c),
		consistencySample(5, sumServer, c),
		300*time.Millisecond,
	)
	if !strings.Contains(out, "DIVERGED") {
		t.Errorf("a mismatch that persisted with nothing in flight must be reported as divergence:\n%s", out)
	}
	if strings.Contains(out, "No divergence found") {
		t.Errorf("summary contradicts the finding:\n%s", out)
	}
}

// TestConsistencyDoesNotReportMidEditAsDiverged is the false positive that
// would make this tool useless: a client applies its own edit locally before
// the server orders it, so a window being typed in legitimately holds different
// content at any instant.
func TestConsistencyDoesNotReportMidEditAsDiverged(t *testing.T) {
	first := consistencySample(5, sumServer,
		client.ClientBufferReport{ClientID: 1, Answered: true, Known: true, Version: 5, Generation: 2, ContentSha256: sumOther})
	// Same mismatch, but the client's own content changed between samples — it
	// is being typed in, not stuck.
	second := consistencySample(5, sumServer,
		client.ClientBufferReport{ClientID: 1, Answered: true, Known: true, Version: 5, Generation: 2, ContentSha256: []byte{0xee, 0xff}})

	out := formatConsistency(first, second, 300*time.Millisecond)
	if strings.Contains(out, "DIVERGED") {
		t.Errorf("an edit in flight must not be reported as divergence:\n%s", out)
	}
	if !strings.Contains(out, "inconclusive") {
		t.Errorf("expected the mismatch to be called inconclusive:\n%s", out)
	}
}

// TestConsistencyServerStillApplyingIsInconclusive covers the other in-flight
// direction: the server's own version moved between samples.
func TestConsistencyServerStillApplyingIsInconclusive(t *testing.T) {
	c := client.ClientBufferReport{ClientID: 1, Answered: true, Known: true, Version: 5, Generation: 2, ContentSha256: sumOther}
	out := formatConsistency(consistencySample(5, sumServer, c), consistencySample(6, sumServer, c), 300*time.Millisecond)
	if strings.Contains(out, "DIVERGED") {
		t.Errorf("the server was still applying ops; that is not divergence:\n%s", out)
	}
}

// TestConsistencyStaleGenerationIsNotDivergence covers the expected,
// self-correcting case: the buffer was swapped wholesale and this client hasn't
// polled yet, so it is about to resync on its own.
func TestConsistencyStaleGenerationIsNotDivergence(t *testing.T) {
	c := client.ClientBufferReport{ClientID: 1, Answered: true, Known: true, Version: 5, Generation: 1, ContentSha256: sumOther}
	out := formatConsistency(consistencySample(5, sumServer, c), consistencySample(5, sumServer, c), 300*time.Millisecond)
	if strings.Contains(out, "DIVERGED") {
		t.Errorf("a stale generation means a resync is pending, not divergence:\n%s", out)
	}
	if !strings.Contains(out, "resync pending") {
		t.Errorf("expected the stale generation to be named:\n%s", out)
	}
}

func TestConsistencyMatchingClientIsOK(t *testing.T) {
	c := client.ClientBufferReport{ClientID: 1, Answered: true, Known: true, Version: 5, Generation: 2, ContentSha256: sumServer}
	out := formatConsistency(consistencySample(5, sumServer, c), consistencySample(5, sumServer, c), 300*time.Millisecond)
	if !strings.Contains(out, "OK") || !strings.Contains(out, "No divergence found") {
		t.Errorf("a matching client should read as OK:\n%s", out)
	}
}

// TestConsistencyDistinguishesNoAnswerFromNotHolding guards the distinction a
// diagnosis depends on: a wedged window and a closed tab are different findings.
func TestConsistencyDistinguishesNoAnswerFromNotHolding(t *testing.T) {
	wedged := client.ClientBufferReport{ClientID: 1, Answered: false}
	closed := client.ClientBufferReport{ClientID: 2, Answered: true, Known: false}
	out := formatConsistency(
		consistencySample(5, sumServer, wedged, closed),
		consistencySample(5, sumServer, wedged, closed),
		300*time.Millisecond,
	)
	if !strings.Contains(out, "NO ANSWER") {
		t.Errorf("a client that never answered must be called out:\n%s", out)
	}
	if !strings.Contains(out, "does not hold this buffer") {
		t.Errorf("a client without the buffer must be reported as such, not as unresponsive:\n%s", out)
	}
	// Only the wedged one counts as a problem.
	if !strings.Contains(out, "1 problem(s) found") {
		t.Errorf("expected exactly one problem:\n%s", out)
	}
}

func TestExecCheckConsistencyRejectsHugeSettle(t *testing.T) {
	out, isErr := execCheckConsistency(context.Background(), nil, "", checkConsistencyInput{SettleMs: maxSettleMs + 1})
	if !isErr || !strings.Contains(out, "too large") {
		t.Errorf("got (%q, %v), want an error about settle_ms being too large", out, isErr)
	}
}

// TestGetSyncEventsRejectsAnUnknownKind covers the worst answer a diagnostic
// can give: an unknown kind matched nothing, and an empty result is reported as
// "no sync events — buffers and clients stayed in step". A mistyped filter
// reported health that was never checked.
func TestGetSyncEventsRejectsAnUnknownKind(t *testing.T) {
	t.Setenv("INDIGO_LOG_DIR", t.TempDir())

	out, isErr := execGetSyncEvents(getSyncEventsInput{Kind: "resync"})
	if !isErr {
		t.Fatalf("an unknown kind was accepted and answered with: %s", out)
	}
	if !strings.Contains(out, "resync_started") {
		t.Errorf("error %q does not list the kinds the caller could have used", out)
	}
}

// TestGetSyncEventsAcceptsEveryDeclaredKind is the other half: the validator
// must not reject a kind the recorder can produce.
func TestGetSyncEventsAcceptsEveryDeclaredKind(t *testing.T) {
	t.Setenv("INDIGO_LOG_DIR", t.TempDir())

	for _, k := range syncevent.Kinds() {
		if _, isErr := execGetSyncEvents(getSyncEventsInput{Kind: string(k)}); isErr {
			t.Errorf("kind %q is declared but the tool rejects it", k)
		}
	}
	// And no filter at all stays valid.
	if _, isErr := execGetSyncEvents(getSyncEventsInput{}); isErr {
		t.Error("an empty kind must mean 'any', not an error")
	}
}
