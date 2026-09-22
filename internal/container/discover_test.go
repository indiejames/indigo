package container

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFindRuntimePrefersPath(t *testing.T) {
	dir := t.TempDir()
	onPath := filepath.Join(dir, "docker")
	if err := os.WriteFile(onPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("INDIGO_DOCKER", "")
	prev := runtimeCandidates
	runtimeCandidates = func() []string { return nil }
	t.Cleanup(func() { runtimeCandidates = prev })
	// The real lookPath, deliberately: this test is about PATH being consulted
	// first, and dir is the only entry in it.

	got, offPath, err := FindRuntime()
	if err != nil {
		t.Fatalf("FindRuntime: %v", err)
	}
	if got != onPath {
		t.Errorf("found %q, want %q", got, onPath)
	}
	if offPath {
		t.Error("a runtime on PATH was reported as off-PATH; the CLI would be told about it needlessly")
	}
}

func TestFindRuntimeReportsWhatIsMissing(t *testing.T) {
	noRuntime(t)

	_, _, err := FindRuntime()
	if err == nil {
		t.Fatal("found a runtime although discovery was disabled")
	}
	for _, want := range []string{"Docker", "Podman", "INDIGO_DOCKER"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to mention %q", err, want)
		}
	}
}

// TestRuntimeCandidatesAreExecutables: a socket is not something to exec, and
// Colima's is in a directory that otherwise looks like a plausible place to
// find one.
func TestRuntimeCandidatesAreExecutables(t *testing.T) {
	for _, p := range defaultRuntimeCandidates() {
		if strings.HasSuffix(p, ".sock") {
			t.Errorf("candidate %q is a socket", p)
		}
	}
}
