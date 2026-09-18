package syncevent

import (
	"strings"
	"testing"
	"time"

	"github.com/indiejames/indigo/internal/debuglog"
)

// TestRecordRoundTripsThroughTheLog is the whole contract: what Record writes,
// Read gets back, with every field intact. The value of putting these in the
// shared log instead of an in-memory ring rests on this being lossless.
func TestRecordRoundTripsThroughTheLog(t *testing.T) {
	t.Setenv("INDIGO_LOG_DIR", t.TempDir())

	Record("server", GenerationMismatch, 7, "/tmp/a b/main.go", "client has 2, server has 3")
	Record("client", ResyncStarted, 7, "/tmp/a b/main.go", "")

	got, err := Read(ReadOptions{})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d events, want 2: %+v", len(got), got)
	}
	first := got[0]
	if first.Component != "server" || first.Kind != GenerationMismatch || first.BufID != 7 {
		t.Errorf("first event = %+v, want server/generation_mismatch/buf 7", first)
	}
	// The path has a space in it deliberately: an unquoted field format would
	// split it and lose everything after "a".
	if first.Path != "/tmp/a b/main.go" {
		t.Errorf("path = %q, want the full path including its space", first.Path)
	}
	if first.Detail != "client has 2, server has 3" {
		t.Errorf("detail = %q, want it intact", first.Detail)
	}
	if first.Time.IsZero() || !first.TimeExact {
		t.Errorf("time = %v (exact=%v), want a real parsed timestamp", first.Time, first.TimeExact)
	}
}

// TestRecordSurvivesAwkwardText checks the quoting holds for the characters
// that would otherwise break the format: the separator, the quote character,
// and a newline — which would turn one event into two log lines and let a
// detail string forge an event.
func TestRecordSurvivesAwkwardText(t *testing.T) {
	t.Setenv("INDIGO_LOG_DIR", t.TempDir())

	nasty := "detail with = and \" and\nSYNC kind=forged buf=99 path=\"\" detail=\"\""
	Record("client", ResyncFailed, 1, "/tmp/x=y.go", nasty)

	got, err := Read(ReadOptions{})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d events, want 1 — a detail string must not be able to forge "+
			"a second event: %+v", len(got), got)
	}
	if got[0].Detail != nasty {
		t.Errorf("detail = %q, want %q", got[0].Detail, nasty)
	}
	if got[0].Path != "/tmp/x=y.go" {
		t.Errorf("path = %q, want it intact through the = sign", got[0].Path)
	}
}

func TestReadFilters(t *testing.T) {
	t.Setenv("INDIGO_LOG_DIR", t.TempDir())

	Record("server", ApplyOpRejected, 1, "/w/a.go", "one")
	Record("client", ResyncStarted, 2, "/w/b.go", "two")
	Record("server", ApplyOpRejected, 2, "/w/b.go", "three")

	cases := []struct {
		name string
		opts ReadOptions
		want int
	}{
		{"no filter", ReadOptions{}, 3},
		{"by kind", ReadOptions{Kind: ApplyOpRejected}, 2},
		{"by buffer", ReadOptions{BufID: 2}, 2},
		{"by path substring", ReadOptions{Path: "b.go"}, 2},
		{"kind and buffer", ReadOptions{Kind: ApplyOpRejected, BufID: 2}, 1},
		{"newest only", ReadOptions{Max: 1}, 1},
		{"kind that did not happen", ReadOptions{Kind: StaleBase}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Read(tc.opts)
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			if len(got) != tc.want {
				t.Errorf("got %d events, want %d: %+v", len(got), tc.want, got)
			}
		})
	}

	// Max keeps the newest, which is what a caller asking for "the last few"
	// means — the opposite would report the start of the incident and drop the
	// end of it.
	got, err := Read(ReadOptions{Max: 1})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got[0].Detail != "three" {
		t.Errorf("Max=1 returned %q, want the newest event %q", got[0].Detail, "three")
	}
}

// TestParseIgnoresOrdinaryLogLines keeps the two streams separate: the shared
// log is mostly prose, and a line that merely mentions the word SYNC is not an
// event.
func TestParseIgnoresOrdinaryLogLines(t *testing.T) {
	for _, text := range []string{
		"ApplyOp REJECTED: buffer 3 generation mismatch",
		"",
		"SYNCHRONIZED something",
		"SYNC buf=3 path=\"\" detail=\"\"", // no kind
	} {
		if ev, ok := Parse(debuglog.Entry{Text: text}); ok {
			t.Errorf("Parse(%q) returned an event %+v, want it ignored", text, ev)
		}
	}
}

// TestParseToleratesUnknownKeys covers schema drift across builds. The writer
// and the reader can be different binaries — this repo's own recurring trap is
// a server someone forgot to restart — so a field added later must not make an
// older reader reject today's lines.
func TestParseToleratesUnknownKeys(t *testing.T) {
	line := Prefix + `kind=resync_ok buf=4 path="/w/a.go" detail="fine" future=12 other="x y"`
	ev, ok := Parse(debuglog.Entry{Text: line, Tag: "client"})
	if !ok {
		t.Fatal("a line with unknown keys was rejected")
	}
	if ev.Kind != ResyncOK || ev.BufID != 4 || ev.Path != "/w/a.go" || ev.Detail != "fine" {
		t.Errorf("event = %+v, want the known fields parsed", ev)
	}
}

func TestReadSinceExcludesOlderEvents(t *testing.T) {
	t.Setenv("INDIGO_LOG_DIR", t.TempDir())
	Record("server", ApplyOpRejected, 1, "/w/a.go", "now")

	got, err := Read(ReadOptions{Since: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d events from the future, want 0", len(got))
	}
}

func TestCountByKind(t *testing.T) {
	counts := CountByKind([]Event{
		{Kind: ResyncStarted}, {Kind: ResyncStarted}, {Kind: ApplyOpRejected},
	})
	if counts[ResyncStarted] != 2 || counts[ApplyOpRejected] != 1 {
		t.Errorf("counts = %v, want 2 resync_started and 1 apply_op_rejected", counts)
	}
}

// TestSplitFieldsKeepsQuotedSpaces is the unit-level version of the path test
// above, for the helper that makes it work.
func TestSplitFieldsKeepsQuotedSpaces(t *testing.T) {
	got := splitFields(`kind=x path="/a b/c.go" detail="one two three"`)
	want := []string{`kind=x`, `path="/a b/c.go"`, `detail="one two three"`}
	if len(got) != len(want) {
		t.Fatalf("got %d fields %q, want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("field %d = %q, want %q", i, got[i], want[i])
		}
	}
	if strings.Contains(got[1], "detail") {
		t.Error("a quoted path swallowed the next field")
	}
}
