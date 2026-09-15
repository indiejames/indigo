package debuglog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestReadKeepsEntriesFromAFileThatFailsToScan is a regression test: Read used
// to drop a file's entries entirely when readFile returned an error, even
// though readFile returns the entries it did parse alongside that error on
// purpose.
//
// The realistic trigger is the one the scanner is sized for. Plugin stderr goes
// into this same file and can carry a single enormous line, and a line past the
// 4MB limit makes bufio.Scanner fail — taking the whole day's log with it under
// the old code, which is exactly the output someone reading a report is looking
// for.
func TestReadKeepsEntriesFromAFileThatFailsToScan(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("INDIGO_LOG_DIR", dir)

	now := time.Now()
	stamp := now.Format(TimeLayout)
	var b strings.Builder
	b.WriteString(stamp + " [server] before the big line\n")
	// Past the 4MB scanner cap, so sc.Err() reports "token too long".
	b.WriteString(strings.Repeat("x", 5*1024*1024) + "\n")
	b.WriteString(stamp + " [server] after the big line\n")

	name := filepath.Join(dir, namePrefix+"-"+now.Format("2006-01-02")+".log")
	if err := os.WriteFile(name, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	entries, err := Read(ReadOptions{})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no entries: a scan failure discarded every line parsed before it")
	}
	if got := entries[0].Text; got != "before the big line" {
		t.Errorf("first entry = %q, want the line preceding the oversized one", got)
	}
}

// TestReadStillSkipsAnUnreadableFile is the complement: a file that yields no
// entries at all must not break the read of the others, which is what the
// original skip was for.
func TestReadStillSkipsAnUnreadableFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("INDIGO_LOG_DIR", dir)

	now := time.Now()
	stamp := now.Format(TimeLayout)

	// A readable file for an earlier date, and an unreadable one for today.
	older := filepath.Join(dir, namePrefix+"-"+now.AddDate(0, 0, -1).Format("2006-01-02")+".log")
	if err := os.WriteFile(older, []byte(stamp+" [server] readable\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	blocked := filepath.Join(dir, namePrefix+"-"+now.Format("2006-01-02")+".log")
	if err := os.WriteFile(blocked, []byte(stamp+" [server] unreadable\n"), 0o000); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root: mode 000 does not prevent reading")
	}

	entries, err := Read(ReadOptions{})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(entries) != 1 || entries[0].Text != "readable" {
		t.Errorf("entries = %+v, want just the readable file's one line", entries)
	}
}
