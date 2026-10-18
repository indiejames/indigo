package client

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestFormatKeepsEditsTypedWhileItRuns: Format used to replace the server's
// buffer with the formatter's output and bump its generation. Every op still
// on its way — typed in this window after the format was requested, or in
// another window at any point before it saw the swap — was then refused as
// belonging to the old buffer, the window resynced, and the edit was gone.
// The output is now applied as ordinary ops, so both edits are rebased past it.
func TestFormatKeepsEditsTypedWhileItRuns(t *testing.T) {
	if _, err := exec.LookPath("gofmt"); err != nil {
		t.Skip("gofmt not on PATH")
	}
	workDir, sock := startOTServer(t)
	path := filepath.Join(workDir, "f.go")
	if err := os.WriteFile(path, []byte("package main\nfunc  main(){}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := dialOTClient(t, sock, path, workDir, "A")
	b := dialOTClient(t, sock, path, workDir, "B")
	gen := a.m.generation

	format := a.m.fetchFormat(false)
	a.edit(t, otInsert(0, 0, "// A\n")) // typed in A after asking for the format
	b.edit(t, otInsert(1, 14, " // B")) // typed in B, not yet sent
	msg := format()                     // the server formats and applies
	if fm, ok := msg.(formatResultMsg); !ok || fm.err != nil || !fm.changed {
		t.Fatalf("format result = %#v, want a change", msg)
	}
	a.integrate(t, msg)
	a.flush(t)
	b.flush(t)

	assertConverged(t, a, b)
	const want = "// A\npackage main\n\nfunc main() {} // B\n"
	if got := a.m.buf.Content(); got != want {
		t.Fatalf("buffer = %q, want %q: the formatting and both edits", got, want)
	}
	if a.m.generation != gen || b.m.generation != gen {
		t.Fatal("generation changed: the format replaced the buffer and the windows resynced")
	}
	if !a.m.Dirty() {
		t.Error("formatted buffer is not marked dirty")
	}
}
