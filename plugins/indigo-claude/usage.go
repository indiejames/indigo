package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// ─── subscription plan usage ──────────────────────────────────────────────────
//
// CLI mode runs on a Claude subscription with a 5-hour rolling session limit
// and a weekly limit. The same endpoint the official clients use for their
// usage display (GET /api/oauth/usage, authorized by the Claude Code OAuth
// token) reports utilization percentages, which the TUI surfaces as warnings
// at 75% and 90%. Unofficial but stable across the official clients; failures
// are silent so a change in the endpoint can never break the TUI.

const usageEndpoint = "https://api.anthropic.com/api/oauth/usage"

// planUsage holds the subscription windows' utilization.
type planUsage struct {
	FiveHourPct   float64
	FiveHourReset time.Time
	SevenDayPct   float64
	SevenDayReset time.Time
}

type planUsageMsg struct{ usage planUsage }

// planWarnLevels are the utilization percentages that trigger a warning, in
// ascending order.
var planWarnLevels = []float64{75, 90}

// crossedLevel returns the highest warn level that pct has reached and that
// exceeds alreadyWarned, or 0 when there is nothing new to warn about.
func crossedLevel(pct, alreadyWarned float64) float64 {
	var crossed float64
	for _, lvl := range planWarnLevels {
		if pct >= lvl && lvl > alreadyWarned {
			crossed = lvl
		}
	}
	return crossed
}

// oauthToken reads the Claude Code OAuth access token: from the login Keychain
// on macOS, from ~/.claude/.credentials.json elsewhere.
func oauthToken() (string, error) {
	var raw []byte
	if runtime.GOOS == "darwin" {
		out, err := exec.Command("security", "find-generic-password",
			"-s", "Claude Code-credentials", "-w").Output()
		if err != nil {
			return "", fmt.Errorf("keychain: %w", err)
		}
		raw = out
	} else {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		data, err := os.ReadFile(filepath.Join(home, ".claude", ".credentials.json"))
		if err != nil {
			return "", err
		}
		raw = data
	}
	var creds struct {
		ClaudeAiOauth struct {
			AccessToken string `json:"accessToken"`
		} `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(raw))), &creds); err != nil {
		return "", err
	}
	if creds.ClaudeAiOauth.AccessToken == "" {
		return "", fmt.Errorf("no access token in credentials")
	}
	return creds.ClaudeAiOauth.AccessToken, nil
}

// parsePlanUsage decodes the /api/oauth/usage response body.
func parsePlanUsage(body []byte) (planUsage, error) {
	var resp struct {
		FiveHour *struct {
			Utilization float64   `json:"utilization"`
			ResetsAt    time.Time `json:"resets_at"`
		} `json:"five_hour"`
		SevenDay *struct {
			Utilization float64   `json:"utilization"`
			ResetsAt    time.Time `json:"resets_at"`
		} `json:"seven_day"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return planUsage{}, err
	}
	var u planUsage
	if resp.FiveHour != nil {
		u.FiveHourPct = resp.FiveHour.Utilization
		u.FiveHourReset = resp.FiveHour.ResetsAt
	}
	if resp.SevenDay != nil {
		u.SevenDayPct = resp.SevenDay.Utilization
		u.SevenDayReset = resp.SevenDay.ResetsAt
	}
	return u, nil
}

// fetchPlanUsage queries the usage endpoint with the stored OAuth token.
func fetchPlanUsage() (planUsage, error) {
	token, err := oauthToken()
	if err != nil {
		return planUsage{}, err
	}
	req, err := http.NewRequest(http.MethodGet, usageEndpoint, nil)
	if err != nil {
		return planUsage{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return planUsage{}, err
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		return planUsage{}, fmt.Errorf("usage endpoint: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return planUsage{}, err
	}
	return parsePlanUsage(body)
}

// fetchPlanUsageCmd fetches plan usage off the UI loop; errors are dropped so
// endpoint changes or missing credentials can never disturb the session.
func fetchPlanUsageCmd() tea.Cmd {
	return func() tea.Msg {
		u, err := fetchPlanUsage()
		if err != nil {
			return nil
		}
		return planUsageMsg{usage: u}
	}
}

// fmtReset renders a reset time compactly: same-day resets show the clock
// time, later resets include the weekday.
func fmtReset(t time.Time) string {
	t = t.Local()
	now := time.Now()
	if t.YearDay() == now.YearDay() && t.Year() == now.Year() {
		return t.Format("15:04")
	}
	return t.Format("Mon 15:04")
}
