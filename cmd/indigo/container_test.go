package main

import (
	"strings"
	"testing"

	"github.com/indiejames/indigo/internal/app"
	"github.com/indiejames/indigo/internal/container"
)

func resetContainerFlags(t *testing.T) {
	t.Helper()
	containerName, containerDir = "", ""
	useDevcontainer = false
	remoteUser, projectServerPath = "", ""
	t.Cleanup(func() {
		containerName, containerDir = "", ""
		useDevcontainer = false
		remoteUser, projectServerPath = "", ""
		pathMap = container.PathMap{}
	})
}

func TestParseContainerFlagsLeavesPositionalArgsAlone(t *testing.T) {
	resetContainerFlags(t)

	got := parseContainerFlags([]string{"--container", "devbox", "+42", "main.go"})
	if strings.Join(got, " ") != "+42 main.go" {
		t.Errorf("remaining args = %v, want the +N and the file untouched", got)
	}
	if containerName != "devbox" {
		t.Errorf("containerName = %q, want devbox", containerName)
	}
}

func TestParseContainerFlagsReadsTheContainerDir(t *testing.T) {
	resetContainerFlags(t)

	got := parseContainerFlags([]string{"--container", "devbox", "--container-dir", "/workspaces/proj", "a.go"})
	if strings.Join(got, " ") != "a.go" {
		t.Errorf("remaining args = %v, want just the file", got)
	}
	if containerDir != "/workspaces/proj" {
		t.Errorf("containerDir = %q, want /workspaces/proj", containerDir)
	}
}

// TestParseContainerFlagsIgnoresFlagsItDoesNotOwn guards the thing a hand-rolled
// parser gets wrong: this runs before the rest of the command's own positional
// parsing, so anything it does not recognise has to come through untouched and
// in order.
func TestParseContainerFlagsIgnoresFlagsItDoesNotOwn(t *testing.T) {
	resetContainerFlags(t)

	in := []string{"--server", "/some/dir"}
	got := parseContainerFlags(in)
	if strings.Join(got, " ") != strings.Join(in, " ") {
		t.Errorf("args = %v, want them unchanged", got)
	}
	if containerName != "" {
		t.Errorf("containerName = %q, want empty", containerName)
	}
}

func TestParseContainerFlagsWithNoContainerArgs(t *testing.T) {
	resetContainerFlags(t)

	got := parseContainerFlags([]string{"main.go"})
	if strings.Join(got, " ") != "main.go" {
		t.Errorf("args = %v, want unchanged", got)
	}
	if containerName != "" || containerDir != "" {
		t.Errorf("flags set from nothing: name=%q dir=%q", containerName, containerDir)
	}
}

func TestResolveWorkspaceIsAPassThroughWithoutAContainer(t *testing.T) {
	resetContainerFlags(t)
	pathMap = container.PathMap{}

	workDir, target := resolveWorkspace("/Users/me/proj", "/Users/me/proj/a.go")
	if workDir != "/Users/me/proj" || target != "/Users/me/proj/a.go" {
		t.Errorf("resolveWorkspace = (%q, %q), want both unchanged", workDir, target)
	}
	if pathMap.HostRoot != "" || pathMap.ContainerRoot != "" {
		t.Errorf("a path map was built without a container: %+v", pathMap)
	}
}

// TestResolveWorkspaceMovesIntoContainerPaths is the whole of bite 4 from the
// command's side: past this point the client works in the container's names,
// which is what lets every one of the RPCs stay untouched.
func TestResolveWorkspaceMovesIntoContainerPaths(t *testing.T) {
	resetContainerFlags(t)
	pathMap = container.PathMap{}
	containerName = "devbox"
	containerDir = "/workspaces/proj"
	app.SetRecentRoot("")
	t.Cleanup(func() { app.SetRecentRoot("") })

	workDir, target := resolveWorkspace("/Users/me/proj", "/Users/me/proj/cmd/a.go")
	if workDir != "/workspaces/proj" {
		t.Errorf("workDir = %q, want the container's path", workDir)
	}
	if target != "/workspaces/proj/cmd/a.go" {
		t.Errorf("target = %q, want it translated", target)
	}
	// The recent-files list stays keyed on the host, so two projects that both
	// mount at the same container path do not share one.
	if pathMap.HostRoot != "/Users/me/proj" {
		t.Errorf("host root = %q, want it remembered for the recent-files key", pathMap.HostRoot)
	}
}

func TestResolveWorkspaceDefaultsTheContainerDirToTheHostPath(t *testing.T) {
	resetContainerFlags(t)
	pathMap = container.PathMap{}
	containerName = "devbox"
	app.SetRecentRoot("")
	t.Cleanup(func() { app.SetRecentRoot("") })

	// The `-v $PWD:$PWD` case: both sides agree, and nothing is rewritten.
	workDir, target := resolveWorkspace("/Users/me/proj", "/Users/me/proj/a.go")
	if workDir != "/Users/me/proj" || target != "/Users/me/proj/a.go" {
		t.Errorf("resolveWorkspace = (%q, %q), want both unchanged", workDir, target)
	}
	if !pathMap.Identity() {
		t.Error("the map should be an identity when no --container-dir is given")
	}
}

func TestResolveWorkspaceHandlesNoTarget(t *testing.T) {
	resetContainerFlags(t)
	pathMap = container.PathMap{}
	containerName = "devbox"
	containerDir = "/workspaces/proj"
	app.SetRecentRoot("")
	t.Cleanup(func() { app.SetRecentRoot("") })

	workDir, target := resolveWorkspace("/Users/me/proj", "")
	if workDir != "/workspaces/proj" {
		t.Errorf("workDir = %q, want the container's path", workDir)
	}
	if target != "" {
		t.Errorf("target = %q, want it left empty", target)
	}
}

func TestParseContainerFlagsReadsDevcontainer(t *testing.T) {
	resetContainerFlags(t)

	got := parseContainerFlags([]string{"--devcontainer", "main.go"})
	if strings.Join(got, " ") != "main.go" {
		t.Errorf("remaining args = %v, want just the file", got)
	}
	if !useDevcontainer {
		t.Error("--devcontainer did not set the flag")
	}
	if containerName != "" {
		t.Errorf("containerName = %q, want it left for the CLI to fill in", containerName)
	}
}
