// Package hangdetect reports work that has stopped making progress, from
// inside the process it stopped in.
//
// The failure this exists for is the one that leaves nothing behind: the
// editor stops responding, the user kills it, and the only evidence is their
// description of it. A stall is not an error — nothing returns, nothing is
// logged, no status message is pushed, and by the time anyone looks the
// process is gone. So the detection has to happen while it is still stuck, and
// it has to write down where it is stuck.
//
// Two things are watched, because "it hangs" has two distinct shapes here and
// each is invisible to the other's detector:
//
//   - A *loop* that stopped beating. Beat is called at the top of the Bubble
//     Tea update loop with the message it is about to process, so a freeze
//     names the message that froze it. That is most of the diagnosis whenever
//     the cause is synchronous work on the UI thread — a subprocess with no
//     timeout, a blocking file lock, a disk read on a stalled mount.
//   - An *operation* that outlived its budget. Track brackets a capnp call in
//     either direction, so a call that never comes back is reported with its
//     method name and how long it has been outstanding. The direction that
//     matters is *incoming*: a server handler has no deadline of its own, so a
//     blocked one blocks for as long as whatever it is blocked on takes, and
//     capnp serialises a connection's calls until a handler releases the queue
//     — so it takes every later call with it. Outgoing calls are a backstop,
//     for the ones made with no deadline at all; see rpcwatch, which measured
//     the rest of that claim rather than assuming it.
//
// Every report carries the full in-flight inventory, not just the one
// operation that tripped it. On a wedged connection the interesting fact is
// not which call stalled but that *all* of them did at once: capnp serialises
// a connection's calls behind a handler that does not release the queue, so
// head-of-line blocking looks like one stuck call and a queue of victims, and
// only the inventory shows that shape.
//
// A first report also carries a goroutine dump. That is the part that names
// the culprit rather than the symptom: a stack shows a parked flock, a
// subprocess wait or a capnp future by file and line, which no amount of
// structured event logging would have produced. Dumps are rate-limited per
// process and capped, because they land in the shared log beside everything
// else. They contain function names, not buffer contents — the same
// constraint report_bundle is held to.
//
// Nothing here can fail an edit. Reporting is best-effort, and the detector is
// inert until Start is called, so tests, plugins and short-lived tools never
// arm it and never pay for it.
package hangdetect

import (
	"context"
	"fmt"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/indiejames/indigo/internal/debuglog"
)

// Prefix marks a line this package wrote. Deliberately distinctive and
// greppable by hand, and the substring to pass to get_logs' contains filter:
// "HANG" appears nowhere else in indigo's output.
const Prefix = "HANG "

// Kind says which detector fired.
type Kind string

const (
	// LoopStalled: a loop that beats on every iteration stopped beating, so
	// whatever it was processing has not returned.
	LoopStalled Kind = "loop_stalled"
	// CallStalled: a tracked operation outlived its budget without finishing.
	CallStalled Kind = "call_stalled"
)

// Phase brackets one episode. A start and an end with no end in between are
// the normal pair; a start with no end at all means the process died stuck,
// which is itself the finding.
type Phase string

const (
	PhaseStart  Phase = "start"
	PhaseUpdate Phase = "update"
	PhaseEnd    Phase = "end"
)

const (
	// scanInterval is how often the monitor looks. Well under LoopThreshold so
	// the reported elapsed time is close to the real one.
	scanInterval = 250 * time.Millisecond

	// LoopThreshold is how long a beating loop may go quiet before it counts
	// as stalled. Two seconds is already a freeze a person notices and
	// remembers, and is far longer than any legitimate single Update — the
	// slowest real work there is a full reparse of a large buffer.
	LoopThreshold = 2 * time.Second

	// RepeatInterval is how often an episode still in progress is re-reported.
	// A stall that lasts minutes should leave a trail showing it lasted
	// minutes, not one line that could have been a blip.
	RepeatInterval = 15 * time.Second

	// CallGrace is added to a call's own deadline before it counts as stalled.
	// A call that merely finishes late is not this package's business; a call
	// still outstanding well past the deadline that was supposed to end it is.
	CallGrace = 2 * time.Second

	// NoDeadlineBudget is used for a call whose context carries no deadline.
	// Longer than any deadline indigo actually sets (30s, for the workspace
	// diagnostics scan), so it cannot be the thing that fires first.
	NoDeadlineBudget = 45 * time.Second

	// ServerCallBudget bounds an incoming call on the server, measured from
	// the moment it is received to the moment it returns — so it includes time
	// spent queued behind an earlier handler, which is the latency a client
	// actually experiences and the thing head-of-line blocking produces.
	// Well above the server's own longest deliberate wait (pluginReadyTimeout,
	// 2s), so a busy server does not trip it.
	ServerCallBudget = 20 * time.Second

	// DumpInterval rate-limits goroutine dumps per process. Two stalls
	// seconds apart are one incident and one dump describes both.
	DumpInterval = 30 * time.Second

	// MaxDumpBytes caps a goroutine dump. Large enough for a server holding
	// language servers and plugins (a few hundred goroutines), small enough
	// that a repeating stall cannot fill a temp directory.
	MaxDumpBytes = 256 << 10
)

// Detector is one process's monitor. Use the package-level functions for the
// process default; construct one directly only in tests, which drive scan
// themselves rather than starting a goroutine.
type Detector struct {
	mu       sync.Mutex
	calls    map[uint64]*trackedCall
	loops    map[string]*trackedLoop
	nextID   uint64
	lastDump time.Time

	armed  atomic.Bool
	stopMu sync.Mutex
	stop   chan struct{}
	done   chan struct{}

	// Seams. Tests replace these; nothing else does.
	clock func() time.Time
	dump  func() []byte
	emit  func(tag, text string)
}

type trackedCall struct {
	tag        string
	what       string
	start      time.Time
	budget     time.Duration
	reported   bool
	lastReport time.Time
}

type trackedLoop struct {
	tag        string
	what       string
	last       time.Time
	threshold  time.Duration
	reported   bool
	lastReport time.Time
	stallStart time.Time
}

// New returns an unarmed detector.
func New() *Detector {
	return &Detector{
		calls: make(map[uint64]*trackedCall),
		loops: make(map[string]*trackedLoop),
		clock: time.Now,
		dump:  goroutineDump,
		emit:  func(tag, text string) { debuglog.Write(tag, "%s", text) },
	}
}

// Start arms the detector and begins scanning. Calling it twice is harmless.
//
// Arming is explicit rather than automatic on first use so that a test, a
// plugin or a one-shot CLI invocation never grows a background goroutine that
// would report the process's own orderly exit as a stall.
func (d *Detector) Start() {
	d.stopMu.Lock()
	defer d.stopMu.Unlock()
	if d.stop != nil {
		return
	}
	d.stop = make(chan struct{})
	d.done = make(chan struct{})
	d.armed.Store(true)
	go d.run(d.stop, d.done)
}

// Stop disarms the detector and waits for the monitor to finish, so a caller
// shutting down cannot race a report into a log it is about to stop using.
//
// Anything still in flight at Stop is dropped without an end line. That is
// deliberate: the process is leaving, and a synthetic "recovered" for work
// that never recovered would be a lie in the one place the log is read.
func (d *Detector) Stop() {
	d.stopMu.Lock()
	stop, done := d.stop, d.done
	d.stop, d.done = nil, nil
	d.stopMu.Unlock()
	if stop == nil {
		return
	}
	d.armed.Store(false)
	close(stop)
	<-done
}

func (d *Detector) run(stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	t := time.NewTicker(scanInterval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			d.scan(d.clock())
		}
	}
}

// Beat records that the loop named tag reached the top of an iteration, about
// to process what. Cheap enough for the UI thread: one atomic load when
// disarmed, one map write and a timestamp when armed.
//
// what is the thing that will be blamed if the loop never beats again, so it
// should name the unit of work — the message type, the request — and not the
// loop.
func (d *Detector) Beat(tag, what string, threshold time.Duration) {
	if !d.armed.Load() {
		return
	}
	now := d.clock()
	d.mu.Lock()
	l := d.loops[tag]
	if l == nil {
		l = &trackedLoop{tag: tag, threshold: threshold}
		d.loops[tag] = l
	}
	// Read before overwriting: the beat that ends a stall names whatever the
	// loop moved on to, and the end line has to name what it was stuck on, or
	// it cannot be paired with the start line it closes.
	blamed := l.what
	l.what = what
	l.last = now
	l.threshold = threshold
	recovered := l.reported
	stalled := now.Sub(l.stallStart)
	if recovered {
		l.reported = false
		l.stallStart = time.Time{}
	}
	d.mu.Unlock()

	if recovered {
		d.emitLine(tag, Prefix+fields(
			"kind", string(LoopStalled), "phase", string(PhaseEnd),
			"tag", tag, "elapsed", dur(stalled), "what", quote(blamed),
		))
	}
}

// StopLoop forgets a loop, for a caller that is shutting it down on purpose.
// Without it the final iteration's beat looks like a stall a moment later.
func (d *Detector) StopLoop(tag string) {
	d.mu.Lock()
	delete(d.loops, tag)
	d.mu.Unlock()
}

// Track registers an operation as in flight and returns the func that retires
// it. The returned func is safe to call more than once and is never nil, so a
// caller can defer it unconditionally.
//
// budget is when the operation becomes a stall, measured from now.
func (d *Detector) Track(tag, what string, budget time.Duration) func() {
	if !d.armed.Load() {
		return func() {}
	}
	now := d.clock()
	d.mu.Lock()
	d.nextID++
	id := d.nextID
	d.calls[id] = &trackedCall{tag: tag, what: what, start: now, budget: budget}
	d.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() { d.retire(id) })
	}
}

func (d *Detector) retire(id uint64) {
	now := d.clock()
	d.mu.Lock()
	c := d.calls[id]
	delete(d.calls, id)
	d.mu.Unlock()
	if c == nil || !c.reported {
		return
	}
	d.emitLine(c.tag, Prefix+fields(
		"kind", string(CallStalled), "phase", string(PhaseEnd),
		"tag", c.tag, "elapsed", dur(now.Sub(c.start)), "what", quote(c.what),
	))
}

// scan is one pass of the monitor. Separate from run so a test can drive it at
// times of its choosing instead of sleeping and hoping.
func (d *Detector) scan(now time.Time) {
	type pending struct {
		kind    Kind
		phase   Phase
		tag     string
		what    string
		elapsed time.Duration
		budget  time.Duration
		// seq orders two entries that started in the same instant, which is
		// ordinary for calls queued behind one another on a connection. Without
		// it their order is the runtime's map order and a report is not
		// reproducible from one run to the next.
		seq uint64
	}
	var due []pending

	d.mu.Lock()
	for _, l := range d.loops {
		if l.last.IsZero() {
			continue
		}
		since := now.Sub(l.last)
		switch {
		case since < l.threshold:
			// Recovery is reported by Beat, which is the moment it happened;
			// reporting it here would date it to the next scan instead.
		case !l.reported:
			l.reported = true
			l.stallStart = l.last
			l.lastReport = now
			due = append(due, pending{LoopStalled, PhaseStart, l.tag, l.what, since, l.threshold, 0})
		case now.Sub(l.lastReport) >= RepeatInterval:
			l.lastReport = now
			due = append(due, pending{LoopStalled, PhaseUpdate, l.tag, l.what, since, l.threshold, 0})
		}
	}
	for id, c := range d.calls {
		elapsed := now.Sub(c.start)
		switch {
		case elapsed < c.budget:
		case !c.reported:
			c.reported = true
			c.lastReport = now
			due = append(due, pending{CallStalled, PhaseStart, c.tag, c.what, elapsed, c.budget, id})
		case now.Sub(c.lastReport) >= RepeatInterval:
			c.lastReport = now
			due = append(due, pending{CallStalled, PhaseUpdate, c.tag, c.what, elapsed, c.budget, id})
		}
	}
	// The inventory is snapshotted here, under the same lock that decided what
	// is due, so the report describes one instant rather than a walk through a
	// map that other goroutines are still editing.
	inventory, inflight := d.inventoryLocked(now)
	wantDump := len(due) > 0 && (d.lastDump.IsZero() || now.Sub(d.lastDump) >= DumpInterval)
	if wantDump {
		d.lastDump = now
	}
	d.mu.Unlock()

	if len(due) == 0 {
		return
	}
	// Oldest first. Both loops above walk maps, so without this the call that
	// heads the report — and so the one carrying the inventory and the dump —
	// is whichever the runtime happened to yield first. When several are due
	// at once they are a wedged call and its victims, and the wedged one is
	// the oldest; leading with a victim buries the finding under its own
	// consequences.
	sort.Slice(due, func(i, j int) bool {
		if due[i].elapsed != due[j].elapsed {
			return due[i].elapsed > due[j].elapsed
		}
		return due[i].seq < due[j].seq
	})
	// The dump is taken outside the lock: runtime.Stack briefly stops the
	// world, and Beat runs on the UI thread.
	var body string
	if wantDump {
		body = formatDump(d.dump())
	}
	for i, p := range due {
		kv := []string{
			"kind", string(p.kind), "phase", string(p.phase), "tag", p.tag,
			"elapsed", dur(p.elapsed), "budget", dur(p.budget),
			"what", quote(p.what), "inflight", strconv.Itoa(inflight),
		}
		text := Prefix + fields(kv...)
		if i == 0 {
			// Inventory and dump ride on the first report of the pass. Several
			// due at once is the head-of-line case, and repeating one dump per
			// victim would bury the log in copies of the same stacks.
			text += inventory + body
		}
		d.emitLine(p.tag, text)
	}
}

// inventoryLocked renders every in-flight operation, oldest first, and returns
// the count alongside. d.mu must be held.
func (d *Detector) inventoryLocked(now time.Time) (string, int) {
	if len(d.calls) == 0 {
		return "\n" + Prefix + "  inflight: none", 0
	}
	type row struct {
		tag, what string
		elapsed   time.Duration
		budget    time.Duration
		over      bool
		seq       uint64
	}
	rows := make([]row, 0, len(d.calls))
	for id, c := range d.calls {
		e := now.Sub(c.start)
		rows = append(rows, row{c.tag, c.what, e, c.budget, e >= c.budget, id})
	}
	// Oldest first, ties broken by the order they were tracked in. Calls
	// queued behind one wedged handler routinely share an instant, and an
	// inventory whose order changes between runs is one nobody can diff.
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].elapsed != rows[j].elapsed {
			return rows[i].elapsed > rows[j].elapsed
		}
		return rows[i].seq < rows[j].seq
	})
	var b strings.Builder
	for _, r := range rows {
		b.WriteString("\n" + Prefix + "  inflight " + fields(
			"tag", r.tag, "what", quote(r.what),
			"elapsed", dur(r.elapsed), "budget", dur(r.budget),
			"over", strconv.FormatBool(r.over),
		))
	}
	return b.String(), len(rows)
}

// emitLine writes one report. Errors are swallowed by debuglog; a diagnostic
// that can fail the thing it is watching is worse than no diagnostic.
func (d *Detector) emitLine(tag, text string) {
	d.emit(tag, text)
}

// formatDump wraps a goroutine dump in a header naming its size, then the
// stacks verbatim.
//
// The stacks are written as plain continuation lines with no timestamp of
// their own, which debuglog.Read already handles — it attributes an
// untimestamped line to the preceding timestamped one, precisely so a trace
// stays attached to the line that explains it.
func formatDump(dump []byte) string {
	if len(dump) == 0 {
		return ""
	}
	n := strings.Count(string(dump), "\ngoroutine ") + 1
	return "\n" + Prefix + "  goroutines follow " + fields(
		"bytes", strconv.Itoa(len(dump)), "count", strconv.Itoa(n),
	) + "\n" + strings.TrimRight(string(dump), "\n")
}

// goroutineDump returns every goroutine's stack, truncated to MaxDumpBytes.
//
// runtime.Stack reports how much it would have written but truncates silently
// at the buffer's end, so the buffer is grown from a starting guess until it
// fits or the cap is reached — a server holding several language servers and
// plugins produces far more than a client window does.
func goroutineDump() []byte {
	size := 64 << 10
	for {
		buf := make([]byte, size)
		n := runtime.Stack(buf, true)
		if n < len(buf) || size >= MaxDumpBytes {
			if n > len(buf) {
				n = len(buf)
			}
			out := buf[:n]
			if len(out) >= MaxDumpBytes {
				out = append(out[:MaxDumpBytes:MaxDumpBytes],
					[]byte("\n...truncated at "+strconv.Itoa(MaxDumpBytes)+" bytes...")...)
			}
			return out
		}
		size *= 2
		if size > MaxDumpBytes {
			size = MaxDumpBytes
		}
	}
}

// BudgetForContext turns a caller's own deadline into a stall budget.
//
// Deriving it from the context rather than picking a constant is what keeps
// this honest across call sites whose deadlines range from 300ms to 30s: the
// question asked is always "is this past the deadline that was supposed to end
// it", never "is this slower than some number chosen here".
func BudgetForContext(ctx context.Context) time.Duration {
	dl, ok := ctx.Deadline()
	if !ok {
		return NoDeadlineBudget
	}
	remaining := time.Until(dl)
	if remaining < 0 {
		remaining = 0
	}
	return remaining + CallGrace
}

// fields renders alternating key/value pairs as "k=v k=v", matching the shape
// syncevent writes so one eye can read both.
func fields(kv ...string) string {
	var b strings.Builder
	for i := 0; i+1 < len(kv); i += 2 {
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(kv[i])
		b.WriteByte('=')
		b.WriteString(kv[i+1])
	}
	return b.String()
}

// quote keeps a value with a space or a newline in it from splitting a line or
// forging a second one — the same reasoning syncevent quotes paths for.
func quote(s string) string { return strconv.Quote(s) }

// dur renders a duration compactly and at a fixed precision, so lines line up
// and a grep for a threshold is not defeated by Go's variable-unit formatting.
func dur(d time.Duration) string {
	return fmt.Sprintf("%.3fs", d.Seconds())
}

// ---- process default ----

var def = New()

// Default returns the process-wide detector.
func Default() *Detector { return def }

// Start arms the process-wide detector. Called once, from the entry point of
// each long-lived process (the editor window, the server).
func Start() { def.Start() }

// Stop disarms it.
func Stop() { def.Stop() }

// Beat records an iteration of the loop named tag. See Detector.Beat.
func Beat(tag, what string, threshold time.Duration) { def.Beat(tag, what, threshold) }

// StopLoop forgets a loop. See Detector.StopLoop.
func StopLoop(tag string) { def.StopLoop(tag) }

// Track brackets an operation. See Detector.Track.
func Track(tag, what string, budget time.Duration) func() { return def.Track(tag, what, budget) }
