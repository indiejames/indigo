package debuglog

import (
	"bufio"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// Entry is one parsed log line.
type Entry struct {
	// Time is the line's own timestamp, or — for a line written straight to
	// the fd from Open, which carries none — the timestamp of the most recent
	// preceding line that had one. Zero only for lines before any timestamped
	// line in the file.
	Time time.Time
	// Exact is false when Time was inherited rather than parsed from this
	// line, so a caller can tell "logged at" from "logged around".
	Exact bool
	Tag   string // "client", "server", "app", "" for untagged/child-process output
	Text  string // the line with timestamp and tag stripped
	Raw   string // the line exactly as it appears in the file
}

// ReadOptions filters what Read returns. The zero value returns everything.
type ReadOptions struct {
	Since    time.Time // drop entries older than this; zero means no lower bound
	Tag      string    // keep only this tag; empty means any
	Contains string    // keep only lines containing this substring; empty means any
	Max      int       // keep only the newest Max entries; 0 means no limit
}

// Read returns log entries from the rotated log files, oldest first.
//
// It reads the whole retained set rather than just today's file, because a
// session that started yesterday evening has its beginning there — and a report
// about something that broke overnight is exactly the case this exists for.
func Read(opts ReadOptions) ([]Entry, error) {
	paths, err := logFiles()
	if err != nil {
		return nil, err
	}

	var out []Entry
	for _, p := range paths {
		// mtime is a cheap prefilter: a file last written to before Since
		// cannot contain an entry at or after it, since writes only append.
		if !opts.Since.IsZero() {
			if fi, err := os.Stat(p); err == nil && fi.ModTime().Before(opts.Since) {
				continue
			}
		}
		entries, err := readFile(p)
		if err != nil && len(entries) == 0 {
			// One unreadable file must not lose the others — a log directory
			// shared with other users (the os.TempDir default) can easily hold
			// a file this process can't open.
			continue
		}
		// A scan error with entries already parsed is kept, not discarded.
		// readFile returns both on purpose, and the realistic error here is a
		// line past the 4MB scanner limit — one oversized line of plugin
		// stderr, which would otherwise take the whole day's log down with it
		// and hide exactly the output someone is looking for.
		for _, e := range entries {
			if !opts.Since.IsZero() && !e.Time.IsZero() && e.Time.Before(opts.Since) {
				continue
			}
			if opts.Tag != "" && e.Tag != opts.Tag {
				continue
			}
			if opts.Contains != "" && !strings.Contains(e.Raw, opts.Contains) {
				continue
			}
			out = append(out, e)
		}
	}

	if opts.Max > 0 && len(out) > opts.Max {
		out = out[len(out)-opts.Max:] // newest Max
	}
	return out, nil
}

// logFiles returns the retained log files in chronological order. The names
// embed an ISO date, so sorting them lexically sorts them by date; the legacy
// un-dated name sorts before all of them, which is correct — it can only hold
// output older than the rotation scheme.
func logFiles() ([]string, error) {
	matches, err := filepath.Glob(filepath.Join(Dir(), namePrefix+"*.log"))
	if err != nil {
		return nil, err
	}
	sort.Strings(matches)
	return matches, nil
}

// readFile parses one log file.
//
// O_NOFOLLOW for the same reason the write path uses it: the default log
// directory is os.TempDir(), which on a multi-user Linux box is a shared /tmp,
// and these names are entirely predictable from the date. Refusing to follow a
// symlink here means another user cannot get this process to read — and then
// hand back to whoever asked — a file it merely happens to have access to.
func readFile(path string) ([]Entry, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close() //nolint:errcheck

	var out []Entry
	var lastTime time.Time
	sc := bufio.NewScanner(f)
	// Plugin stderr lands in this file too and can carry a very long line (a
	// stack trace, a minified source snippet), well past Scanner's 64KB
	// default. Truncating diagnostics is worse than the memory.
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		e := Entry{Raw: line, Text: line}
		if ts, rest, ok := splitTimestamp(line); ok {
			e.Time, e.Exact, e.Text = ts, true, rest
			lastTime = ts
		} else {
			// A line straight from a child process's stderr. Attributing it to
			// the last timestamp we saw is what makes it usable: it is almost
			// always a continuation of whatever was just logged, and dropping
			// it from a time-filtered view would hide precisely the panic
			// output someone is looking for.
			e.Time = lastTime
		}
		e.Tag, e.Text = splitTag(e.Text)
		out = append(out, e)
	}
	if err := sc.Err(); err != nil {
		return out, err
	}
	return out, nil
}

// splitTimestamp pulls a leading TimeLayout timestamp off a line.
func splitTimestamp(line string) (time.Time, string, bool) {
	sp := strings.IndexByte(line, ' ')
	if sp < 0 {
		return time.Time{}, line, false
	}
	ts, err := time.Parse(TimeLayout, line[:sp])
	if err != nil {
		return time.Time{}, line, false
	}
	return ts, line[sp+1:], true
}

// splitTag pulls a leading "[tag] " off a line.
func splitTag(text string) (tag, rest string) {
	if !strings.HasPrefix(text, "[") {
		return "", text
	}
	end := strings.IndexByte(text, ']')
	if end < 0 {
		return "", text
	}
	return text[1:end], strings.TrimPrefix(text[end+1:], " ")
}
