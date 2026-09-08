package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCrossedLevel(t *testing.T) {
	cases := []struct {
		pct, warned, want float64
	}{
		{50, 0, 0},   // below all thresholds
		{75, 0, 75},  // hits 75
		{79, 0, 75},  // between thresholds
		{79, 75, 0},  // already warned at 75
		{90, 75, 90}, // escalates to 90
		{95, 90, 0},  // already warned at 90
		{95, 0, 90},  // jumps straight past both → warn at highest
		{74.9, 0, 0}, // just under
		{100, 90, 0}, // maxed but already warned
	}
	for _, c := range cases {
		if got := crossedLevel(c.pct, c.warned); got != c.want {
			t.Errorf("crossedLevel(%v, %v) = %v, want %v", c.pct, c.warned, got, c.want)
		}
	}
}

// Shape captured from a live /api/oauth/usage response (2026-07).
const sampleUsageJSON = `{
  "five_hour": {"utilization": 77.0, "resets_at": "2026-07-15T08:59:59.726765+00:00",
    "limit_dollars": null, "used_dollars": null, "remaining_dollars": null},
  "seven_day": {"utilization": 79.0, "resets_at": "2026-07-18T20:59:59.726788+00:00",
    "limit_dollars": null, "used_dollars": null, "remaining_dollars": null},
  "seven_day_opus": null,
  "extra_usage": {"is_enabled": true, "monthly_limit": 2000}
}`

func TestParsePlanUsage(t *testing.T) {
	u, err := parsePlanUsage([]byte(sampleUsageJSON))
	if err != nil {
		t.Fatalf("parsePlanUsage: %v", err)
	}
	if u.FiveHourPct != 77.0 || u.SevenDayPct != 79.0 {
		t.Errorf("utilization = %v/%v, want 77/79", u.FiveHourPct, u.SevenDayPct)
	}
	wantReset := time.Date(2026, 7, 18, 20, 59, 59, 726788000, time.UTC)
	if !u.SevenDayReset.Equal(wantReset) {
		t.Errorf("SevenDayReset = %v, want %v", u.SevenDayReset, wantReset)
	}
}

func TestPlanWarnHint(t *testing.T) {
	reset := time.Now().Add(3 * time.Hour)

	m := Model{}
	if got := m.planWarnHint(); got != "" {
		t.Errorf("no data: hint = %q, want empty", got)
	}

	m.plan = planUsage{SevenDayPct: 50, FiveHourPct: 50}
	if got := m.planWarnHint(); got != "" {
		t.Errorf("below threshold: hint = %q, want empty", got)
	}

	m.plan = planUsage{SevenDayPct: 79, SevenDayReset: reset}
	got := m.planWarnHint()
	if !strings.Contains(got, "weekly") || !strings.Contains(got, "79%") {
		t.Errorf("weekly-only hint = %q", got)
	}

	m.plan = planUsage{SevenDayPct: 79, SevenDayReset: reset, FiveHourPct: 90, FiveHourReset: reset}
	got = m.planWarnHint()
	if !strings.Contains(got, "5h 90%") || !strings.Contains(got, "weekly 79%") {
		t.Errorf("both-windows hint = %q", got)
	}
}

// Regression test: the hook must be a no-op for Claude Code sessions that
// aren't indigo-claude's own subprocess, or their Bash commands pop approval
// dialogs in the indigo-claude TUI.
func TestHookCommandGuardsForeignSessions(t *testing.T) {
	cmd := hookCommand("/bin/indigo-claude", "/tmp/x.sock")
	if !strings.Contains(cmd, `[ "$INDIGO_CLAUDE_HOOK" = "1" ] || exit 0`) {
		t.Errorf("hook command missing env guard:\n%s", cmd)
	}
	if !strings.Contains(cmd, `exec '/bin/indigo-claude' --hook '/tmp/x.sock'`) {
		t.Errorf("hook command missing forward line:\n%s", cmd)
	}
	if !isOurHookEntry(map[string]any{
		"hooks": []any{map[string]any{"command": cmd}},
	}) {
		t.Errorf("install/remove would not recognize its own entry:\n%s", cmd)
	}
}

// Regression test for the bug that made every Bash call in a workspace fail
// with "No such file or directory": the hook used to point at a generated
// script under $TMPDIR, which macOS's tmp reaper deletes by access time, and
// which a crashed session never cleaned up either. A hook whose socket or
// binary is missing must exit 0 silently — no decision, no error — so Claude
// Code falls back to its own permission flow.
func TestHookCommandInertWhenSocketOrBinaryMissing(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "indigo-claude")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho SHOULD_NOT_RUN\n"), 0700); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name      string
		bin, sock string
	}{
		{"socket gone", bin, filepath.Join(dir, "absent.sock")},
		{"binary gone", filepath.Join(dir, "absent-binary"), filepath.Join(dir, "absent.sock")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := exec.Command("/bin/sh", "-c", hookCommand(tc.bin, tc.sock))
			c.Env = append(os.Environ(), "INDIGO_CLAUDE_HOOK=1")
			out, err := c.CombinedOutput()
			if err != nil {
				t.Errorf("stale hook must exit 0, got %v (output %q)", err, out)
			}
			if len(out) != 0 {
				t.Errorf("stale hook must produce no output, got %q", out)
			}
		})
	}
}

// The runtime directory comes from os.MkdirTemp and the binary from
// os.Executable; either can sit under a path with spaces, which would split
// into separate words in the registered shell command.
func TestHookCommandQuotesPathsWithSpaces(t *testing.T) {
	dir := t.TempDir() + "/with space"
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	c := exec.Command("/bin/sh", "-c", hookCommand(dir+"/bin", dir+"/perm.sock"))
	c.Env = append(os.Environ(), "INDIGO_CLAUDE_HOOK=1")
	if out, err := c.CombinedOutput(); err != nil || len(out) != 0 {
		t.Errorf("spaced paths: err=%v output=%q", err, out)
	}
}

func TestParsePlanUsageNullWindows(t *testing.T) {
	u, err := parsePlanUsage([]byte(`{"five_hour": null, "seven_day": null}`))
	if err != nil {
		t.Fatalf("parsePlanUsage: %v", err)
	}
	if u.FiveHourPct != 0 || u.SevenDayPct != 0 {
		t.Errorf("null windows should parse as zero, got %+v", u)
	}
}
