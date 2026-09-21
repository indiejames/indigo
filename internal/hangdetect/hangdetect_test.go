package hangdetect

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/indiejames/indigo/internal/debuglog"
)

// newTestDetector returns an armed detector with the clock, the dump and the
// log all under the test's control, and with the monitor goroutine *not*
// running: scan is driven directly, so every assertion here is about what the
// detector decides rather than about whether a sleep was long enough.
func newTestDetector(t *testing.T) (*Detector, *time.Time, *[]string) {
	t.Helper()
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	var lines []string
	d := New()
	d.clock = func() time.Time { return now }
	d.dump = func() []byte { return []byte("goroutine 1 [running]:\nmain.main()\n") }
	d.emit = func(tag, text string) { lines = append(lines, tag+"|"+text) }
	d.armed.Store(true)
	return d, &now, &lines
}

func findLine(lines []string, substrs ...string) string {
	for _, l := range lines {
		ok := true
		for _, s := range substrs {
			if !strings.Contains(l, s) {
				ok = false
				break
			}
		}
		if ok {
			return l
		}
	}
	return ""
}

func TestLoopStallIsReportedOnceThenRecovered(t *testing.T) {
	d, now, lines := newTestDetector(t)

	d.Beat("app", "app.tickMsg", LoopThreshold)

	// Still beating: nothing to say.
	*now = now.Add(LoopThreshold / 2)
	d.scan(*now)
	if len(*lines) != 0 {
		t.Fatalf("reported while the loop was still beating: %v", *lines)
	}

	// The loop stops. One start line, and only one however many scans run.
	*now = now.Add(LoopThreshold)
	d.scan(*now)
	d.scan(now.Add(100 * time.Millisecond))
	start := findLine(*lines, "kind=loop_stalled", "phase=start")
	if start == "" {
		t.Fatalf("no start line for a stalled loop: %v", *lines)
	}
	if !strings.Contains(start, `what="app.tickMsg"`) {
		t.Errorf("start line does not name the message that froze the loop: %s", start)
	}
	if n := len(*lines); n != 1 {
		t.Fatalf("got %d lines, want exactly 1 — a stall must not be re-reported every scan: %v", n, *lines)
	}

	// The loop comes back. Recovery is reported by Beat, at the moment it
	// happened, and carries how long the stall lasted.
	*now = now.Add(5 * time.Second)
	d.Beat("app", "app.tickMsg", LoopThreshold)
	end := findLine(*lines, "kind=loop_stalled", "phase=end")
	if end == "" {
		t.Fatalf("no end line after the loop resumed: %v", *lines)
	}
	// The whole quiet period, measured from the last beat to this one — not
	// from when the monitor first noticed, which is a scan interval later and
	// would under-report every stall by up to that much.
	if !strings.Contains(end, "elapsed=8.000s") {
		t.Errorf("end line should report the whole quiet period, got: %s", end)
	}

	// And the episode is over: a later scan says nothing more.
	before := len(*lines)
	d.scan(*now)
	if len(*lines) != before {
		t.Errorf("reported again after recovery: %v", (*lines)[before:])
	}
}

func TestLoopStallIsReReportedWhileItPersists(t *testing.T) {
	d, now, lines := newTestDetector(t)
	d.Beat("app", "app.bufferOpenedMsg", LoopThreshold)

	*now = now.Add(LoopThreshold + time.Second)
	d.scan(*now)
	*now = now.Add(RepeatInterval - time.Second)
	d.scan(*now) // not yet due
	if n := len(*lines); n != 1 {
		t.Fatalf("got %d lines before RepeatInterval elapsed, want 1: %v", n, *lines)
	}
	*now = now.Add(2 * time.Second)
	d.scan(*now)
	if findLine(*lines, "kind=loop_stalled", "phase=update") == "" {
		t.Fatalf("a stall lasting minutes must leave a trail, not one line: %v", *lines)
	}
}

func TestTrackedCallReportedOnlyWhenPastItsBudget(t *testing.T) {
	d, now, lines := newTestDetector(t)

	fast := d.Track("client", "rpc EditorService.getUpdates", 2*time.Second)
	*now = now.Add(time.Second)
	fast()
	d.scan(*now)
	if len(*lines) != 0 {
		t.Fatalf("a call that finished inside its budget must be silent: %v", *lines)
	}

	slow := d.Track("client", "rpc EditorService.openFile", 5*time.Second)
	*now = now.Add(6 * time.Second)
	d.scan(*now)
	start := findLine(*lines, "kind=call_stalled", "phase=start")
	if start == "" {
		t.Fatalf("no start line for a call past its budget: %v", *lines)
	}
	if !strings.Contains(start, `what="rpc EditorService.openFile"`) {
		t.Errorf("start line does not name the stalled call: %s", start)
	}
	if !strings.Contains(start, "budget=5.000s") {
		t.Errorf("start line does not carry the budget it blew: %s", start)
	}

	*now = now.Add(30 * time.Second)
	slow()
	end := findLine(*lines, "kind=call_stalled", "phase=end")
	if end == "" {
		t.Fatalf("no end line when a reported call finally returned: %v", *lines)
	}
	if !strings.Contains(end, "elapsed=36.000s") {
		t.Errorf("end line should report the call's whole lifetime, got: %s", end)
	}
}

// TestReportCarriesEveryInFlightCall pins the property that makes a report
// usable on a wedged connection: capnp serialises a connection's calls, so the
// call that stalled is rarely the interesting one — the queue of victims
// behind it is. A report naming only the trigger would hide that shape.
func TestReportCarriesEveryInFlightCall(t *testing.T) {
	d, now, lines := newTestDetector(t)

	defer d.Track("server", "rpc EditorService.openFile", 20*time.Second)()
	*now = now.Add(time.Second)
	defer d.Track("server", "rpc EditorService.applyOp", 20*time.Second)()
	defer d.Track("server", "rpc EditorService.getUpdates", 20*time.Second)()

	*now = now.Add(20 * time.Second)
	d.scan(*now)

	report := findLine(*lines, "kind=call_stalled", "phase=start")
	if report == "" {
		t.Fatalf("nothing reported: %v", *lines)
	}
	for _, want := range []string{"openFile", "applyOp", "getUpdates"} {
		if !strings.Contains(report, "inflight tag=server what=\"rpc EditorService."+want+"\"") {
			t.Errorf("inventory is missing %s:\n%s", want, report)
		}
	}
	if !strings.Contains(report, "inflight=3") {
		t.Errorf("report does not count the in-flight calls:\n%s", report)
	}
	// The oldest call heads the report, and the inventory below it is ordered
	// oldest-first — the head of the queue is what a reader needs to see, and
	// both loops feeding this walk maps, so neither order comes for free.
	if !strings.HasPrefix(report, "server|"+Prefix) ||
		!strings.Contains(strings.SplitN(report, "\n", 2)[0], `what="rpc EditorService.openFile"`) {
		t.Errorf("the oldest stalled call does not head the report:\n%s", report)
	}
	inventory := report[strings.Index(report, "inflight tag="):]
	openIdx := strings.Index(inventory, "openFile")
	applyIdx := strings.Index(inventory, "applyOp")
	getIdx := strings.Index(inventory, "getUpdates")
	if openIdx < 0 || applyIdx < 0 || getIdx < 0 || openIdx > applyIdx || applyIdx > getIdx {
		t.Errorf("inventory is not ordered oldest-first:\n%s", inventory)
	}
}

// TestSeveralStallsInOnePassShareOneDump is the head-of-line case again: every
// victim is reported, but repeating the same goroutine dump once per victim
// would bury the log in copies of itself.
func TestSeveralStallsInOnePassShareOneDump(t *testing.T) {
	d, now, lines := newTestDetector(t)
	defer d.Track("client", "rpc EditorService.applyOp", time.Second)()
	defer d.Track("client", "rpc EditorService.getUpdates", time.Second)()

	*now = now.Add(2 * time.Second)
	d.scan(*now)

	if len(*lines) != 2 {
		t.Fatalf("got %d lines, want one per stalled call: %v", len(*lines), *lines)
	}
	dumps := 0
	for _, l := range *lines {
		if strings.Contains(l, "goroutines follow") {
			dumps++
		}
	}
	if dumps != 1 {
		t.Errorf("got %d dumps in one pass, want 1", dumps)
	}
}

func TestGoroutineDumpsAreRateLimited(t *testing.T) {
	d, now, lines := newTestDetector(t)

	stop1 := d.Track("client", "rpc a", time.Second)
	*now = now.Add(2 * time.Second)
	d.scan(*now)
	stop1()

	// A second, separate incident well inside DumpInterval: reported, but the
	// stacks are not written again.
	*now = now.Add(time.Second)
	stop2 := d.Track("client", "rpc b", time.Second)
	*now = now.Add(2 * time.Second)
	d.scan(*now)
	stop2()

	// And a third, past DumpInterval, which does dump again — the process may
	// be stuck somewhere entirely different by then.
	*now = now.Add(DumpInterval)
	defer d.Track("client", "rpc c", time.Second)()
	*now = now.Add(2 * time.Second)
	d.scan(*now)

	dumps := 0
	for _, l := range *lines {
		if strings.Contains(l, "goroutines follow") {
			dumps++
		}
	}
	if dumps != 2 {
		t.Errorf("got %d dumps, want 2 (one per DumpInterval window): %v", dumps, *lines)
	}
}

func TestDisarmedDetectorIsInert(t *testing.T) {
	d, now, lines := newTestDetector(t)
	d.armed.Store(false)

	d.Beat("app", "app.tickMsg", LoopThreshold)
	done := d.Track("client", "rpc openFile", time.Second)
	if done == nil {
		t.Fatal("Track must return a callable even when disarmed")
	}
	done()
	done() // retiring twice must not panic

	*now = now.Add(time.Hour)
	d.scan(*now)
	if len(*lines) != 0 {
		t.Errorf("a disarmed detector wrote to the log: %v", *lines)
	}
}

func TestBudgetForContextFollowsTheCallersOwnDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got := BudgetForContext(ctx)
	if got < 5*time.Second+CallGrace-time.Second || got > 5*time.Second+CallGrace {
		t.Errorf("BudgetForContext = %v, want about %v", got, 5*time.Second+CallGrace)
	}

	if got := BudgetForContext(context.Background()); got != NoDeadlineBudget {
		t.Errorf("BudgetForContext(no deadline) = %v, want %v", got, NoDeadlineBudget)
	}

	// An already-expired deadline still gets the grace period rather than a
	// zero budget, so a call reported as stalled is genuinely overdue and not
	// merely unlucky about when it was tracked.
	expired, cancel2 := context.WithTimeout(context.Background(), -time.Second)
	defer cancel2()
	if got := BudgetForContext(expired); got != CallGrace {
		t.Errorf("BudgetForContext(expired) = %v, want %v", got, CallGrace)
	}
}

// TestReportsReachTheSharedLog drives the real emit path. The seams the other
// tests use are worth nothing if the default detector writes somewhere nobody
// reads, and debuglog is where every other component's evidence already is.
func TestReportsReachTheSharedLog(t *testing.T) {
	t.Setenv("INDIGO_LOG_DIR", t.TempDir())

	d := New()
	d.armed.Store(true)
	d.dump = func() []byte { return []byte("goroutine 99 [chan receive]:\nfake.Frame()\n") }
	start := time.Now()
	defer d.Track("client", "rpc EditorService.openFile", -time.Second)()
	d.scan(time.Now())

	entries, err := debuglog.Read(debuglog.ReadOptions{Since: start.Add(-time.Minute)})
	if err != nil {
		t.Fatalf("debuglog.Read: %v", err)
	}
	var header, frame debuglog.Entry
	for _, e := range entries {
		if strings.HasPrefix(e.Text, Prefix) && strings.Contains(e.Text, "phase=start") {
			header = e
		}
		if strings.Contains(e.Raw, "fake.Frame()") {
			frame = e
		}
	}
	if header.Raw == "" {
		t.Fatalf("no HANG line in the shared log; got %d entries", len(entries))
	}
	if header.Tag != "client" {
		t.Errorf("header tag = %q, want the component's own tag so it sorts in with its prose", header.Tag)
	}
	if !header.Exact {
		t.Error("header line carries no timestamp of its own, so it cannot be lined up against anything")
	}
	// The dump rides as continuation lines, which debuglog dates from the line
	// above — the same treatment a panic trace already gets, and the reason a
	// stack stays attached to the report that explains it.
	if frame.Raw == "" {
		t.Fatal("the goroutine dump did not reach the log")
	}
	if frame.Exact {
		t.Error("a dump line should inherit its time, not claim one")
	}
	if !frame.Time.Equal(header.Time) {
		t.Errorf("dump line time %v is not the header's %v", frame.Time, header.Time)
	}
}

func TestGoroutineDumpNamesThisTest(t *testing.T) {
	dump := string(goroutineDump())
	if !strings.Contains(dump, "TestGoroutineDumpNamesThisTest") {
		t.Error("the dump does not contain the calling goroutine's own stack")
	}
	if !strings.Contains(dump, "goroutine ") {
		t.Error("the dump does not look like a goroutine dump")
	}
	if len(dump) > MaxDumpBytes+128 {
		t.Errorf("dump is %d bytes, past the %d cap", len(dump), MaxDumpBytes)
	}
}

func TestStopWaitsForTheMonitor(t *testing.T) {
	d := New()
	d.Start()
	d.Start() // idempotent
	d.Beat("app", "app.tickMsg", LoopThreshold)
	d.Stop()
	d.Stop() // idempotent
	if d.armed.Load() {
		t.Error("detector still armed after Stop")
	}
}

func TestQuotingKeepsAValueFromForgingASecondField(t *testing.T) {
	d, now, lines := newTestDetector(t)
	d.Beat("app", "app.tickMsg\nHANG kind=loop_stalled phase=end", LoopThreshold)
	*now = now.Add(2 * LoopThreshold)
	d.scan(*now)
	if len(*lines) != 1 {
		t.Fatalf("got %d lines: %v", len(*lines), *lines)
	}
	if strings.Contains((*lines)[0], "\nHANG kind=loop_stalled phase=end") {
		t.Errorf("an embedded newline forged a second event: %s", (*lines)[0])
	}
}
