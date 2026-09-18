// Package syncevent records buffer-synchronization events in a form a tool can
// filter and count, rather than only a human can read.
//
// These are the moments where a client and the server disagree and something
// has to give: an op refused, a buffer swapped underneath a client, a resync, a
// response arriving too late to use. They are exactly what a report like "my
// second window stopped updating" needs and exactly what is hardest to
// reconstruct afterwards, because the on-screen evidence is a status message
// that was overwritten seconds later.
//
// # Why the shared log rather than an in-memory ring
//
// The original plan called for a bounded ring on each side, shipped to the
// server through a new capnp callback. That was written before debuglog gained
// timestamps and rotation-spanning reads, and the log turns out to be the
// better carrier on every axis that matters here:
//
//   - The client, the server, the app and every plugin already append to one
//     file, unbuffered and O_APPEND, so events from different processes are
//     already interleaved in one ordered stream. A ring per process needs an
//     RPC to collect, and needs the process to still be alive to answer.
//   - A ring dies with the process. Half the value of this is diagnosing a
//     window that has since been closed, or a server that exited overnight.
//   - Rotation and the 24h prune bound the storage already.
//
// What a ring offered over prose logging was machine-readability, and that is a
// property of the *line format*, not of where the line is kept. So these lines
// carry a stable, parseable prefix and Parse reads them back.
package syncevent

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/indiejames/indigo/internal/debuglog"
)

// Prefix marks a line as a machine-readable sync event. It is deliberately
// distinctive: get_logs' contains filter is the fallback for anything this
// package does not model, and "SYNC " is greppable by hand.
const Prefix = "SYNC "

// Kind classifies an event. These are closed rather than free-form so a tool
// can count by kind and a caller cannot silently invent a synonym for one that
// already exists.
type Kind string

const (
	// ApplyOpRejected: the server refused an op outright. The detail says why.
	ApplyOpRejected Kind = "apply_op_rejected"
	// GenerationMismatch: a client's view of the buffer object is stale — the
	// server replaced it wholesale (format-on-save, SaveAs, DiscardRecovery,
	// Format, reload) since the client last looked.
	GenerationMismatch Kind = "generation_mismatch"
	// StaleBase: an op arrived based on a version older than what that client
	// has already acknowledged, so the ops needed to rebase it are gone.
	StaleBase Kind = "stale_base"
	// ResyncStarted / ResyncOK / ResyncFailed bracket a client throwing away
	// its buffer and refetching. Ordinary concurrent editing must not produce
	// these; after OT they mean a wholesale swap or an RPC failure.
	ResyncStarted Kind = "resync_started"
	ResyncOK      Kind = "resync_ok"
	ResyncFailed  Kind = "resync_failed"
	// DuplicateOpsSkipped: a poll response carried ops this client had already
	// applied, because two polls were outstanding at once. Harmless once, and a
	// sign the client is polling faster than it can consume if it is constant.
	DuplicateOpsSkipped Kind = "duplicate_ops_skipped"
	// StaleResponseDropped: a poll response from before a swap this client has
	// already resynced past.
	StaleResponseDropped Kind = "stale_response_dropped"
	// SendFailed: an op could not be sent at all, which forces a resync.
	SendFailed Kind = "send_failed"
	// ExternalWriteNotified: the server saw a file change on disk and told the
	// clients holding it.
	ExternalWriteNotified Kind = "external_write_notified"
	// BufferReloaded: a buffer's content was replaced from disk.
	BufferReloaded Kind = "buffer_reloaded"
)

// Kinds returns every kind, in a stable order, for validating a caller's filter
// and for telling them what they could have asked for.
//
// Lives here rather than in the tool so adding a kind above cannot leave a
// validator behind that rejects it — the failure would be a filter that reports
// "no events" for something that is happening, which is the worst answer a
// diagnostic can give.
func Kinds() []Kind {
	return []Kind{
		ApplyOpRejected, GenerationMismatch, StaleBase,
		ResyncStarted, ResyncOK, ResyncFailed,
		DuplicateOpsSkipped, StaleResponseDropped, SendFailed,
		ExternalWriteNotified, BufferReloaded,
	}
}

// ValidKind reports whether k is one of Kinds().
func ValidKind(k Kind) bool {
	for _, known := range Kinds() {
		if k == known {
			return true
		}
	}
	return false
}

// Event is one recorded occurrence.
type Event struct {
	Time      time.Time
	TimeExact bool   // false when inherited from a preceding line — see debuglog.Read
	Component string // the debuglog tag that wrote it: client, server, app
	Kind      Kind
	BufID     uint32
	Path      string
	Detail    string
}

// Record writes one event to the shared log.
//
// tag is the component name the caller already uses for its own debuglog
// writes, so events sort into the same stream as the prose around them — which
// is the point: the line before a resync is usually what explains it.
//
// Values are quoted, so a path with a space or a detail with an equals sign
// round-trips. Recording is best-effort and never returns an error: a
// diagnostic that can fail an edit is worse than no diagnostic.
func Record(tag string, k Kind, bufID uint32, path, detail string) {
	debuglog.Write(tag, "%skind=%s buf=%d path=%s detail=%s",
		Prefix, k, bufID, strconv.Quote(path), strconv.Quote(detail))
}

// Recordf is Record with a formatted detail.
func Recordf(tag string, k Kind, bufID uint32, path, format string, args ...any) {
	Record(tag, k, bufID, path, fmt.Sprintf(format, args...))
}

// Parse reads back a line Record wrote. It reports false for any other line,
// which is how a caller separates events from the surrounding prose.
//
// Unknown keys are ignored rather than rejected, so a field added later does
// not make an older reader refuse today's lines — the same reasoning the capnp
// schema relies on, for the same reason: the writer and the reader here can be
// different builds, one of them a server someone forgot to restart.
func Parse(e debuglog.Entry) (Event, bool) {
	if !strings.HasPrefix(e.Text, Prefix) {
		return Event{}, false
	}
	ev := Event{Time: e.Time, TimeExact: e.Exact, Component: e.Tag}
	for _, f := range splitFields(strings.TrimPrefix(e.Text, Prefix)) {
		key, val, ok := strings.Cut(f, "=")
		if !ok {
			continue
		}
		if unquoted, err := strconv.Unquote(val); err == nil {
			val = unquoted
		}
		switch key {
		case "kind":
			ev.Kind = Kind(val)
		case "buf":
			if n, err := strconv.ParseUint(val, 10, 32); err == nil {
				ev.BufID = uint32(n)
			}
		case "path":
			ev.Path = val
		case "detail":
			ev.Detail = val
		}
	}
	if ev.Kind == "" {
		return Event{}, false
	}
	return ev, true
}

// splitFields splits on spaces that are not inside a quoted value. strings.Fields
// would cut a quoted path or detail in half at its first space, which is most of
// the reason those are quoted.
func splitFields(s string) []string {
	var out []string
	var cur strings.Builder
	inQuotes, escaped := false, false
	for _, r := range s {
		switch {
		case escaped:
			escaped = false
			cur.WriteRune(r)
		case r == '\\' && inQuotes:
			escaped = true
			cur.WriteRune(r)
		case r == '"':
			inQuotes = !inQuotes
			cur.WriteRune(r)
		case r == ' ' && !inQuotes:
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// Read returns the events matching opts, oldest first.
//
// It reads through debuglog, so it spans rotated files and inherits the mtime
// prefilter — a report about something that broke before midnight is still
// reachable.
type ReadOptions struct {
	Since    time.Time
	Kind     Kind   // empty means any
	BufID    uint32 // zero means any; buffer ids start at 1
	Path     string // substring match; empty means any
	Max      int    // newest Max events; 0 means no limit
	MaxLines int    // cap on log lines scanned; 0 uses a default
}

func Read(opts ReadOptions) ([]Event, error) {
	maxLines := opts.MaxLines
	if maxLines <= 0 {
		maxLines = 200000
	}
	// Contains: cheap prefilter in debuglog so a busy log is not fully parsed
	// here. It is a substring test on the raw line, and Prefix appears only in
	// lines Record wrote.
	entries, err := debuglog.Read(debuglog.ReadOptions{
		Since:    opts.Since,
		Contains: Prefix,
		Max:      maxLines,
	})
	if err != nil {
		return nil, err
	}
	var out []Event
	for _, e := range entries {
		ev, ok := Parse(e)
		if !ok {
			continue
		}
		if opts.Kind != "" && ev.Kind != opts.Kind {
			continue
		}
		if opts.BufID != 0 && ev.BufID != opts.BufID {
			continue
		}
		if opts.Path != "" && !strings.Contains(ev.Path, opts.Path) {
			continue
		}
		out = append(out, ev)
	}
	if opts.Max > 0 && len(out) > opts.Max {
		out = out[len(out)-opts.Max:]
	}
	return out, nil
}

// CountByKind summarises events, for the headline a long list would otherwise
// bury.
func CountByKind(events []Event) map[Kind]int {
	counts := make(map[Kind]int, len(events))
	for _, e := range events {
		counts[e.Kind]++
	}
	return counts
}
