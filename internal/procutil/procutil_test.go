package procutil

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// processIsGone reports whether pid no longer exists, or exists only as a
// zombie. syscall.Kill(pid, 0) can't tell these apart from "genuinely still
// running" — a zombie still occupies a process-table slot until its parent
// reaps it, so kill(pid, 0) succeeds for it same as a live process. This
// test isn't the grandchild's parent (it gets reparented once the shell
// that forked it is killed), so it can't force a reap and shouldn't wait on
// however long the new parent (the OS's subreaper) takes to get to it —
// a zombie is, for this test's purposes, already gone.
func processIsGone(pid int) bool {
	out, err := exec.Command("ps", "-o", "state=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		// ps exits non-zero once the pid doesn't exist at all.
		return true
	}
	state := strings.TrimSpace(string(out))
	return state == "" || strings.HasPrefix(state, "Z")
}

// TestKillGroupKillsGrandchildren is a regression test: SetPgid+KillGroup
// exist because a plain cmd.Process.Kill() only signals the direct child,
// leaving any process it itself forked (e.g. a linter/formatter that spawns
// worker processes, or here a shell that backgrounds a sleep) running after
// timeout/shutdown. The shell backgrounds "sleep 60", prints its PID, then
// waits on it; SetPgid puts both the shell and its background child in one
// process group (fork doesn't reassign group membership), so KillGroup must
// take out the grandchild too, not just the shell.
func TestKillGroupKillsGrandchildren(t *testing.T) {
	cmd := exec.Command("sh", "-c", "sleep 60 & echo $! ; wait")
	SetPgid(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	var childPID int
	if _, err := fmt.Fscan(stdout, &childPID); err != nil {
		cmd.Process.Kill() //nolint:errcheck
		t.Fatalf("failed to read grandchild pid: %v", err)
	}

	if processIsGone(childPID) {
		t.Fatalf("grandchild %d not running before KillGroup", childPID)
	}

	if err := KillGroup(cmd); err != nil {
		t.Fatalf("KillGroup: %v", err)
	}
	cmd.Wait() //nolint:errcheck

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if processIsGone(childPID) {
			return // grandchild is gone — success
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("grandchild %d still running after KillGroup — SetPgid/KillGroup only affected the direct child", childPID)
}

// TestKillGroupFallsBackWithoutPgid verifies KillGroup doesn't error out
// when the process was never put in its own group (SetPgid not called) —
// it falls back to killing just the direct process.
func TestKillGroupFallsBackWithoutPgid(t *testing.T) {
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := KillGroup(cmd); err != nil {
		t.Fatalf("KillGroup without SetPgid: %v", err)
	}
	cmd.Wait() //nolint:errcheck
}
