package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/indiejames/indigo/internal/container"
)

var testSession = container.Session{ContainerID: "0123456789abcdef", WorkspaceDir: "/workspaces/api"}

func TestDecideJoin(t *testing.T) {
	cases := []struct {
		name                           string
		autoJoin, canJoin, interactive bool
		input                          string
		want                           bool
		wantOut                        string // substring; "" means no output
	}{
		{name: "auto join", autoJoin: true, canJoin: true, want: true},
		{name: "enter accepts", canJoin: true, interactive: true, input: "\n", want: true, wantOut: "[Y/n]"},
		{name: "yes", canJoin: true, interactive: true, input: "yes\n", want: true, wantOut: "[Y/n]"},
		{name: "no", canJoin: true, interactive: true, input: "n\n", want: false, wantOut: "last save wins"},
		{name: "eof is not yes", canJoin: true, interactive: true, input: "", want: false, wantOut: "[Y/n]"},
		{name: "no terminal warns", canJoin: true, want: false, wantOut: "devcontainer_auto_join"},
		// Without the CLI, joining is impossible even with auto join set.
		{name: "no cli", autoJoin: true, interactive: true, input: "y\n", want: false, wantOut: "Install the devcontainer CLI"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var out bytes.Buffer
			got := decideJoin(c.autoJoin, c.canJoin, c.interactive, strings.NewReader(c.input), &out,
				"/Users/me/src/api", testSession)
			if got != c.want {
				t.Errorf("decideJoin = %v, want %v", got, c.want)
			}
			if c.wantOut == "" {
				if out.Len() != 0 {
					t.Errorf("unexpected output %q", out.String())
				}
			} else if !strings.Contains(out.String(), c.wantOut) {
				t.Errorf("output %q does not contain %q", out.String(), c.wantOut)
			}
			if !c.interactive && strings.Contains(out.String(), "[Y/n]") {
				t.Error("asked a question with no terminal to answer it")
			}
		})
	}
}

// TestDevcontainerStartMessage: --devcontainer used to say "starting dev
// container ... this can take a while" even when it was about to join a
// session already running, which reads as though that session is about to be
// replaced.
func TestDevcontainerStartMessage(t *testing.T) {
	const dir = "/Users/me/src/api"
	cases := map[devcontainerState]string{
		devcontainerSession: "joining the indigo session",
		devcontainerRunning: "attaching to the running dev container",
		devcontainerUnknown: "starting dev container",
	}
	for state, want := range cases {
		got := devcontainerStartMessage(dir, state)
		if !strings.Contains(got, want) {
			t.Errorf("state %d: %q, want it to contain %q", state, got, want)
		}
		if state != devcontainerUnknown && strings.Contains(got, "starting") {
			t.Errorf("state %d: %q says starting for a container that is already running", state, got)
		}
	}
}

// TestWorkspaceInContainer covers `indigo --devcontainer` run from a
// subdirectory. `devcontainer up` has to be given the folder holding
// devcontainer.json, and reports where *that* folder is mounted; using that
// as the workspace would open the repository root instead of the folder
// asked for.
func TestWorkspaceInContainer(t *testing.T) {
	const root = "/Users/me/src/api"
	cases := []struct {
		name, hostWorkDir, sessionDir, want string
	}{
		{"the config's own folder", root, "", "/workspaces/api"},
		{"a subdirectory", root + "/cmd/server", "", "/workspaces/api/cmd/server"},
		// The session's path is what its server was started on, and the
		// bridge has to name exactly that.
		{"joining a running session", root, "/workspaces/api", "/workspaces/api"},
		{"outside the config's folder", "/Users/me/elsewhere", "", "/workspaces/api"},
	}
	for _, c := range cases {
		if got := workspaceInContainer("/workspaces/api", root, c.hostWorkDir, c.sessionDir); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
