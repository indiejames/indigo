package agenttools

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/indiejames/indigo/internal/client"
	"github.com/indiejames/indigo/internal/debuglog"
	"github.com/indiejames/indigo/internal/syncevent"
)

// ─── get_logs ─────────────────────────────────────────────────────────────────

type getLogsInput struct {
	Since    string `json:"since"`
	Tag      string `json:"tag"`
	Contains string `json:"contains"`
	MaxLines int    `json:"max_lines"`
}

const (
	defaultLogSince = 30 * time.Minute
	defaultLogLines = 200
	maxLogLines     = 2000
)

func execGetLogs(in getLogsInput) (string, bool) {
	opts := debuglog.ReadOptions{Tag: in.Tag, Contains: in.Contains, Max: in.MaxLines}

	since := defaultLogSince
	if in.Since != "" {
		d, err := time.ParseDuration(in.Since)
		if err != nil {
			return fmt.Sprintf("bad since %q: %v (use a duration like 15m, 2h)", in.Since, err), true
		}
		if d <= 0 {
			return fmt.Sprintf("bad since %q: must be positive", in.Since), true
		}
		since = d
	}
	opts.Since = time.Now().Add(-since)

	if opts.Max <= 0 {
		opts.Max = defaultLogLines
	}
	// Capped rather than rejected: the caller asking for everything should get
	// the most recent everything, not an error.
	if opts.Max > maxLogLines {
		opts.Max = maxLogLines
	}

	entries, err := debuglog.Read(opts)
	if err != nil {
		return fmt.Sprintf("cannot read logs from %s: %v", debuglog.Dir(), err), true
	}
	if len(entries) == 0 {
		return fmt.Sprintf("no matching log lines in the last %s (log dir: %s)", since, debuglog.Dir()), false
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%d line(s) from %s, last %s", len(entries), debuglog.Dir(), since)
	if opts.Tag != "" {
		fmt.Fprintf(&b, ", tag=%s", opts.Tag)
	}
	if opts.Contains != "" {
		fmt.Fprintf(&b, ", containing %q", opts.Contains)
	}
	if len(entries) == opts.Max {
		// Say so explicitly: silently truncating diagnostics is how someone
		// concludes an event didn't happen when it simply fell off the top.
		fmt.Fprintf(&b, " (truncated to the newest %d — raise max_lines or narrow the filters)", opts.Max)
	}
	b.WriteString("\n\n")
	for _, e := range entries {
		b.WriteString(e.Raw)
		b.WriteByte('\n')
	}
	return b.String(), false
}

// ─── get_sync_state ───────────────────────────────────────────────────────────

type getSyncStateInput struct {
	Path string `json:"path"`
}

func execGetSyncState(ctx context.Context, rpc *client.RPC, workDir string, in getSyncStateInput) (string, bool) {
	states, err := rpc.GetSyncState(ctx, 0)
	if err != nil {
		return fmt.Sprintf("cannot read sync state: %v", err), true
	}
	if in.Path != "" {
		want := absPath(workDir, in.Path)
		filtered := states[:0:0]
		for _, s := range states {
			if s.Path == want {
				filtered = append(filtered, s)
			}
		}
		if len(filtered) == 0 {
			return fmt.Sprintf("%s is not open on the server (open buffers: %d)", in.Path, len(states)), false
		}
		states = filtered
	}
	if len(states) == 0 {
		return "no buffers are open on the server", false
	}
	return formatSyncState(states), false
}

// formatSyncState renders sync state as text, flagging the two conditions that
// actually indicate a problem rather than leaving them to be spotted by eye.
func formatSyncState(states []client.BufferSyncState) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d open buffer(s)\n", len(states))
	for _, s := range states {
		path := s.Path
		if path == "" {
			path = "(untitled)"
		}
		fmt.Fprintf(&b, "\nbuffer %d  %s\n", s.BufID, path)
		fmt.Fprintf(&b, "  version=%d generation=%d dirty=%v lines=%d bytes=%d sha256=%s\n",
			s.Version, s.Generation, s.Dirty, s.LineCount, s.ContentBytes, shortHash(s.ContentSha256))
		fmt.Fprintf(&b, "  retained ops=%d\n", s.HistoryLen)
		if len(s.Clients) == 0 {
			b.WriteString("  clients: none (buffer is orphaned)\n")
			continue
		}
		b.WriteString("  clients:\n")
		for _, c := range s.Clients {
			behind := ""
			if c.AckedVersion < s.Version {
				// The signal worth surfacing: a client this far back has
				// stopped consuming ops, which both blocks history trimming
				// and means its window is showing stale content.
				behind = fmt.Sprintf("  <-- %d version(s) behind", s.Version-c.AckedVersion)
			}
			fmt.Fprintf(&b, "    client %d (conn %d) acked=%d%s\n", c.ClientID, c.ConnID, c.AckedVersion, behind)
		}
	}
	return b.String()
}

func shortHash(sum []byte) string {
	if len(sum) == 0 {
		return "(none)"
	}
	h := hex.EncodeToString(sum)
	if len(h) > 12 {
		h = h[:12]
	}
	return h
}

// ─── report_bundle ────────────────────────────────────────────────────────────

type reportBundleInput struct {
	Since string `json:"since"`
}

// execReportBundle writes one diagnostic file and returns its path plus a short
// summary, rather than returning the whole thing. A bundle is long by design —
// dumping it inline would bury the summary, and the point is to produce
// something a person can attach to a report.
func execReportBundle(ctx context.Context, rpc *client.RPC, workDir string, in reportBundleInput) (string, bool) {
	since := time.Hour
	if in.Since != "" {
		d, err := time.ParseDuration(in.Since)
		if err != nil || d <= 0 {
			return fmt.Sprintf("bad since %q: use a positive duration like 30m, 2h", in.Since), true
		}
		since = d
	}

	var b strings.Builder
	fmt.Fprintf(&b, "indigo diagnostic bundle\ngenerated: %s\nworkdir:   %s\nlog dir:   %s\nwindow:    last %s\n",
		time.Now().Format(time.RFC3339), workDir, debuglog.Dir(), since)
	b.WriteString("\nNo buffer contents are included — buffers appear as sha256 and byte count only.\n")
	b.WriteString("Log lines may still contain file paths and plugin output; review before sharing.\n")

	b.WriteString("\n═══ sync state ═══\n\n")
	states, err := rpc.GetSyncState(ctx, 0)
	switch {
	case err != nil:
		fmt.Fprintf(&b, "unavailable: %v\n", err)
	case len(states) == 0:
		b.WriteString("no buffers are open on the server\n")
	default:
		b.WriteString(formatSyncState(states))
	}

	b.WriteString("\n═══ logs ═══\n\n")
	entries, err := debuglog.Read(debuglog.ReadOptions{Since: time.Now().Add(-since), Max: maxLogLines})
	if err != nil {
		fmt.Fprintf(&b, "unavailable: %v\n", err)
	} else {
		if len(entries) == maxLogLines {
			fmt.Fprintf(&b, "(truncated to the newest %d lines)\n", maxLogLines)
		}
		for _, e := range entries {
			b.WriteString(e.Raw)
			b.WriteByte('\n')
		}
	}

	// Written into the log directory rather than the workspace: a bundle is
	// throwaway diagnostic output and has no business turning up in someone's
	// git status.
	// os.CreateTemp rather than os.WriteFile to a name built from the clock.
	// That name is entirely predictable and this directory is os.TempDir(),
	// which on a multi-user Linux box is a shared /tmp — the same reasoning
	// that makes debuglog open its files O_NOFOLLOW. CreateTemp opens with
	// O_CREATE|O_EXCL and a random suffix, so it cannot be made to follow a
	// symlink another user planted or to append into a file they pre-created.
	// It also removes the collision between two bundles written in one second.
	// Named separately from err: that one still holds the log-read result,
	// which the summary below reports on.
	f, createErr := os.CreateTemp(debuglog.Dir(), "indigo-report-"+time.Now().Format("20060102-150405")+"-*.txt")
	if createErr != nil {
		return fmt.Sprintf("cannot write bundle: %v", createErr), true
	}
	name := f.Name()
	if _, writeErr := f.WriteString(b.String()); writeErr != nil {
		f.Close() //nolint:errcheck
		return fmt.Sprintf("cannot write bundle: %v", writeErr), true
	}
	// Close is checked: a write can fail on flush, and reporting a path that
	// holds a truncated bundle is worse than reporting the failure.
	if closeErr := f.Close(); closeErr != nil {
		return fmt.Sprintf("cannot write bundle: %v", closeErr), true
	}

	summary := fmt.Sprintf("wrote %s (%d bytes)\n  buffers: %d\n  log lines: %d (last %s)",
		name, b.Len(), len(states), len(entries), since)
	if err == nil && len(states) > 0 {
		if n := buffersWithLaggingClients(states); n > 0 {
			summary += fmt.Sprintf("\n  NOTE: %d buffer(s) have a client behind the current version", n)
		}
	}
	return summary, false
}

func buffersWithLaggingClients(states []client.BufferSyncState) int {
	n := 0
	for _, s := range states {
		for _, c := range s.Clients {
			if c.AckedVersion < s.Version {
				n++
				break
			}
		}
	}
	return n
}

// ─── check_buffer_consistency ─────────────────────────────────────────────────

type checkConsistencyInput struct {
	Path     string `json:"path"`
	SettleMs int    `json:"settle_ms"`
}

const (
	defaultSettleMs = 300
	maxSettleMs     = 5000
)

// execCheckConsistency samples consistency twice and reports only mismatches
// that survive both samples unchanged.
//
// One sample cannot tell divergence from ordinary editing. A client applies its
// own edit locally before the server orders it, and its version only catches up
// on the next poll, so at any instant a window being typed in legitimately holds
// different content from the server. What is *not* legitimate is a mismatch that
// persists while neither side's version moves: nothing is in flight, both sides
// have stopped changing, and they still disagree. That is divergence, and it is
// otherwise invisible — no error, no version mismatch, no generation change.
func execCheckConsistency(ctx context.Context, rpc *client.RPC, workDir string, in checkConsistencyInput) (string, bool) {
	settle := time.Duration(defaultSettleMs) * time.Millisecond
	if in.SettleMs > 0 {
		if in.SettleMs > maxSettleMs {
			return fmt.Sprintf("settle_ms %d is too large (max %d)", in.SettleMs, maxSettleMs), true
		}
		settle = time.Duration(in.SettleMs) * time.Millisecond
	}

	first, err := rpc.CheckBufferConsistency(ctx, 0)
	if err != nil {
		return fmt.Sprintf("consistency check failed: %v", err), true
	}
	select {
	case <-time.After(settle):
	case <-ctx.Done():
		return "consistency check cancelled while settling", true
	}
	second, err := rpc.CheckBufferConsistency(ctx, 0)
	if err != nil {
		return fmt.Sprintf("second consistency sample failed: %v", err), true
	}

	if in.Path != "" {
		want := absPath(workDir, in.Path)
		first, second = filterByPath(first, want), filterByPath(second, want)
		if len(second) == 0 {
			return fmt.Sprintf("%s is not open on the server", in.Path), false
		}
	}
	if len(second) == 0 {
		return "no buffers are open on the server", false
	}
	return formatConsistency(first, second, settle), false
}

func filterByPath(in []client.BufferConsistency, path string) []client.BufferConsistency {
	out := in[:0:0]
	for _, b := range in {
		if b.Path == path {
			out = append(out, b)
		}
	}
	return out
}

func formatConsistency(first, second []client.BufferConsistency, settle time.Duration) string {
	firstByBuf := map[uint32]client.BufferConsistency{}
	for _, b := range first {
		firstByBuf[b.BufID] = b
	}

	var b strings.Builder
	fmt.Fprintf(&b, "consistency check across %d buffer(s), two samples %s apart\n", len(second), settle)
	problems := 0

	for _, cur := range second {
		path := cur.Path
		if path == "" {
			path = "(untitled)"
		}
		fmt.Fprintf(&b, "\nbuffer %d  %s\n", cur.BufID, path)
		fmt.Fprintf(&b, "  server: version=%d generation=%d sha256=%s\n",
			cur.ServerVersion, cur.ServerGeneration, shortHash(cur.ServerSha256))
		if len(cur.Clients) == 0 {
			b.WriteString("  no clients hold this buffer\n")
			continue
		}
		prev, hadPrev := firstByBuf[cur.BufID]
		for _, c := range cur.Clients {
			switch {
			case !c.Answered:
				fmt.Fprintf(&b, "  client %d: NO ANSWER (window wedged, or shutting down)\n", c.ClientID)
				problems++
				continue
			case !c.Known:
				fmt.Fprintf(&b, "  client %d: does not hold this buffer\n", c.ClientID)
				continue
			}
			match := bytes.Equal(c.ContentSha256, cur.ServerSha256)
			fmt.Fprintf(&b, "  client %d: version=%d generation=%d dirty=%v sha256=%s",
				c.ClientID, c.Version, c.Generation, c.Dirty, shortHash(c.ContentSha256))
			switch {
			case match:
				b.WriteString("  OK\n")
			case c.Generation != cur.ServerGeneration:
				// Expected and self-correcting: the buffer was swapped
				// wholesale and this client hasn't polled yet, so it is about
				// to resync. Not divergence.
				b.WriteString("  differs (stale generation — resync pending)\n")
			case !hadPrev || stillSettling(prev, cur, c):
				b.WriteString("  differs (edit in flight — inconclusive)\n")
			default:
				b.WriteString("  DIVERGED (mismatch persisted with nothing in flight)\n")
				problems++
			}
		}
	}

	if problems == 0 {
		b.WriteString("\nNo divergence found.\n")
	} else {
		fmt.Fprintf(&b, "\n%d problem(s) found — see the DIVERGED / NO ANSWER lines above.\n", problems)
	}
	return b.String()
}

// stillSettling reports whether anything changed between the two samples for
// this client, which makes a mismatch inconclusive rather than divergence.
func stillSettling(prev, cur client.BufferConsistency, c client.ClientBufferReport) bool {
	if prev.ServerVersion != cur.ServerVersion {
		return true // the server was still applying ops
	}
	for _, p := range prev.Clients {
		if p.ClientID != c.ClientID {
			continue
		}
		// Its own version moving, or its content changing, both mean the client
		// was mid-edit rather than stuck holding something wrong.
		return p.Version != c.Version || !bytes.Equal(p.ContentSha256, c.ContentSha256)
	}
	return true // not present in the first sample; no baseline to compare
}

// ---- get_sync_events ----

type getSyncEventsInput struct {
	Since string `json:"since"`
	Kind  string `json:"kind"`
	Path  string `json:"path"`
	BufID uint32 `json:"buf_id"`
	Max   int    `json:"max"`
}

// execGetSyncEvents reports the sync events recorded by every process.
//
// Deliberately reads the shared log rather than asking the server for an
// in-memory ring: client, server and app all append to one file, so their
// events are already one ordered stream, and they survive the process that
// wrote them. A window that has since been closed — or a server that exited
// overnight — is exactly the case a report arrives about. See internal/syncevent.
func execGetSyncEvents(in getSyncEventsInput) (string, bool) {
	since := defaultLogSince
	if in.Since != "" {
		d, err := time.ParseDuration(in.Since)
		if err != nil {
			return fmt.Sprintf("bad since %q: %v (use a duration like 15m, 2h)", in.Since, err), true
		}
		if d <= 0 {
			return fmt.Sprintf("bad since %q: must be positive", in.Since), true
		}
		since = d
	}
	maxEvents := in.Max
	if maxEvents <= 0 {
		maxEvents = defaultLogLines
	}
	if maxEvents > maxLogLines {
		maxEvents = maxLogLines
	}

	// Validated rather than passed through. An unknown kind matches nothing,
	// and the empty result reads as "no sync events — buffers and clients
	// stayed in step": a typo would report health. Reporting health that was
	// never checked is the one answer a diagnostic must never give.
	if in.Kind != "" && !syncevent.ValidKind(syncevent.Kind(in.Kind)) {
		known := make([]string, 0, len(syncevent.Kinds()))
		for _, k := range syncevent.Kinds() {
			known = append(known, string(k))
		}
		return fmt.Sprintf("unknown kind %q: use one of %s",
			in.Kind, strings.Join(known, ", ")), true
	}

	events, err := syncevent.Read(syncevent.ReadOptions{
		Since: time.Now().Add(-since),
		Kind:  syncevent.Kind(in.Kind),
		BufID: in.BufID,
		Path:  in.Path,
		Max:   maxEvents,
	})
	if err != nil {
		return fmt.Sprintf("cannot read logs from %s: %v", debuglog.Dir(), err), true
	}
	if len(events) == 0 {
		// Said positively: no events in this window is the healthy answer, and
		// a caller checking whether something went wrong should not have to
		// guess whether an empty result means "nothing happened" or "nothing
		// was recorded".
		return fmt.Sprintf("no sync events in the last %s%s — "+
			"buffers and clients stayed in step, or nothing was open",
			since, filterSuffix(in)), false
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%d sync event(s) in the last %s%s\n\n", len(events), since, filterSuffix(in))

	counts := syncevent.CountByKind(events)
	kinds := make([]string, 0, len(counts))
	for k := range counts {
		kinds = append(kinds, string(k))
	}
	sort.Strings(kinds)
	b.WriteString("by kind:\n")
	for _, k := range kinds {
		fmt.Fprintf(&b, "  %-24s %d\n", k, counts[syncevent.Kind(k)])
	}

	b.WriteString("\nevents (oldest first):\n")
	for _, e := range events {
		ts := e.Time.Format("15:04:05.000")
		if !e.TimeExact {
			// Marked rather than hidden: an inherited timestamp is close enough
			// to be useful and wrong enough to mislead if it looks exact.
			ts = "~" + ts
		}
		fmt.Fprintf(&b, "  %s [%s] %s buf=%d", ts, e.Component, e.Kind, e.BufID)
		if e.Path != "" {
			fmt.Fprintf(&b, " %s", filepath.Base(e.Path))
		}
		if e.Detail != "" {
			fmt.Fprintf(&b, " — %s", e.Detail)
		}
		b.WriteByte('\n')
	}
	return b.String(), false
}

func filterSuffix(in getSyncEventsInput) string {
	var parts []string
	if in.Kind != "" {
		parts = append(parts, "kind="+in.Kind)
	}
	if in.BufID != 0 {
		parts = append(parts, fmt.Sprintf("buf=%d", in.BufID))
	}
	if in.Path != "" {
		parts = append(parts, "path~"+in.Path)
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, ", ") + ")"
}
