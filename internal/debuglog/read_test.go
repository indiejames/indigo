package debuglog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriteTimestampsLines(t *testing.T) {
	useTempDir(t)
	before := time.Now().Add(-time.Second)
	Write("server", "hello")

	entries, err := Read(ReadOptions{})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1: %+v", len(entries), entries)
	}
	e := entries[0]
	if !e.Exact {
		t.Error("entry has no parsed timestamp; Write must emit one")
	}
	if e.Time.Before(before) || e.Time.After(time.Now().Add(time.Second)) {
		t.Errorf("entry time = %v, want ~now", e.Time)
	}
	if e.Tag != "server" || e.Text != "hello" {
		t.Errorf("entry = {Tag:%q, Text:%q}, want {server, hello}", e.Tag, e.Text)
	}
}

func TestReadFiltersByTagAndSubstring(t *testing.T) {
	useTempDir(t)
	Write("client", "ApplyOp FAILED buf=3")
	Write("server", "ApplyOp REJECTED buf=3")
	Write("client", "unrelated chatter")

	byTag, err := Read(ReadOptions{Tag: "client"})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(byTag) != 2 {
		t.Errorf("tag filter returned %d entries, want 2", len(byTag))
	}

	bySubstr, err := Read(ReadOptions{Contains: "buf=3"})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(bySubstr) != 2 {
		t.Errorf("substring filter returned %d entries, want 2", len(bySubstr))
	}

	both, err := Read(ReadOptions{Tag: "server", Contains: "REJECTED"})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(both) != 1 || !strings.Contains(both[0].Text, "REJECTED") {
		t.Errorf("combined filter returned %+v, want the one server REJECTED line", both)
	}
}

func TestReadMaxKeepsNewest(t *testing.T) {
	useTempDir(t)
	for i := 0; i < 5; i++ {
		Write("app", "line %d", i)
	}
	entries, err := Read(ReadOptions{Max: 2})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	if entries[0].Text != "line 3" || entries[1].Text != "line 4" {
		t.Errorf("entries = [%q %q], want the newest two [line 3, line 4]", entries[0].Text, entries[1].Text)
	}
}

// TestReadInheritsTimestampForChildProcessLines covers output written straight
// to the fd from Open — a plugin's or the server's stderr — which carries no
// timestamp of its own. Dropping those from a time-filtered view would hide
// exactly the panic output someone is chasing, so they inherit the preceding
// line's time and are marked inexact.
func TestReadInheritsTimestampForChildProcessLines(t *testing.T) {
	dir := useTempDir(t)
	Write("server", "spawning plugin")
	f, err := os.OpenFile(Path(), logOpenFlags, logFileMode)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("panic: something went wrong\n") //nolint:errcheck
	f.Close()                                      //nolint:errcheck
	_ = dir

	entries, err := Read(ReadOptions{Since: time.Now().Add(-time.Minute)})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2: %+v", len(entries), entries)
	}
	child := entries[1]
	if child.Text != "panic: something went wrong" {
		t.Errorf("child line text = %q", child.Text)
	}
	if child.Exact {
		t.Error("child line reported an exact timestamp; it has none of its own")
	}
	if !child.Time.Equal(entries[0].Time) {
		t.Errorf("child line time = %v, want the preceding line's %v", child.Time, entries[0].Time)
	}
}

// TestReadSpansRotatedFiles verifies a session that began before midnight is
// still reachable — the case a report about an overnight failure depends on.
func TestReadSpansRotatedFiles(t *testing.T) {
	dir := useTempDir(t)
	yesterday := time.Now().AddDate(0, 0, -1)
	old := filepath.Join(dir, namePrefix+"-"+yesterday.Format("2006-01-02")+".log")
	line := yesterday.Format(TimeLayout) + " [server] from yesterday\n"
	if err := os.WriteFile(old, []byte(line), logFileMode); err != nil {
		t.Fatal(err)
	}
	Write("server", "from today")

	entries, err := Read(ReadOptions{})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2 (one per file): %+v", len(entries), entries)
	}
	if entries[0].Text != "from yesterday" || entries[1].Text != "from today" {
		t.Errorf("entries = [%q %q], want them oldest-first across files", entries[0].Text, entries[1].Text)
	}

	// Since must also exclude the older file.
	recent, err := Read(ReadOptions{Since: time.Now().Add(-time.Hour)})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(recent) != 1 || recent[0].Text != "from today" {
		t.Errorf("Since filter returned %+v, want only today's line", recent)
	}
}
