package agenttools

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/indiejames/indigo/internal/client"
	"github.com/indiejames/indigo/internal/debuglog"
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
	name := filepath.Join(debuglog.Dir(), "indigo-report-"+time.Now().Format("20060102-150405")+".txt")
	if err := os.WriteFile(name, []byte(b.String()), 0o600); err != nil {
		return fmt.Sprintf("cannot write bundle: %v", err), true
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
