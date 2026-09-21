package container

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeCLI writes the given stdout/stderr and exits with the given code, so the
// CLI wrapper can be driven without the real devcontainer CLI installed.
func fakeCLI(t *testing.T, stdout, stderr string, exitCode int) CLI {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "devcontainer")
	script := "#!/bin/sh\n" +
		"cat <<'STDOUT_EOF'\n" + stdout + "\nSTDOUT_EOF\n" +
		"cat >&2 <<'STDERR_EOF'\n" + stderr + "\nSTDERR_EOF\n" +
		"exit " + itoa(exitCode) + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return CLI{Command: path}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	return string(rune('0' + n))
}

func TestUpParsesTheResult(t *testing.T) {
	cli := fakeCLI(t, `{"outcome":"success","containerId":"abc123","remoteUser":"vscode","remoteWorkspaceFolder":"/workspaces/proj"}`, "", 0)

	res, err := cli.Up(context.Background(), "/Users/me/proj")
	if err != nil {
		t.Fatalf("Up: %v", err)
	}
	if res.ContainerID != "abc123" || res.RemoteUser != "vscode" || res.RemoteWorkspaceFolder != "/workspaces/proj" {
		t.Errorf("result = %+v, want the four facts the connection needs", res)
	}
}

// TestUpIgnoresProgressOutput is the reason the result is parsed line-wise and
// last-match-wins: the CLI interleaves progress records with its result, and
// which stream each lands on has changed between versions. Decoding the whole
// stream, or taking the first object, breaks on both.
func TestUpIgnoresProgressOutput(t *testing.T) {
	out := strings.Join([]string{
		`{"type":"start","detail":"Resolving Dev Container"}`,
		`not json at all`,
		`{"type":"text","text":"Building..."}`,
		`{"outcome":"success","containerId":"abc123","remoteUser":"node","remoteWorkspaceFolder":"/workspaces/x"}`,
	}, "\n")
	cli := fakeCLI(t, out, "", 0)

	res, err := cli.Up(context.Background(), "/w")
	if err != nil {
		t.Fatalf("Up: %v", err)
	}
	if res.ContainerID != "abc123" {
		t.Errorf("containerId = %q, want it picked out of the progress noise", res.ContainerID)
	}
}

func TestUpReportsAFailedOutcome(t *testing.T) {
	cli := fakeCLI(t, `{"outcome":"error","message":"build failed","description":"Dockerfile line 3"}`, "", 1)

	_, err := cli.Up(context.Background(), "/w")
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"build failed", "Dockerfile line 3"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to carry %q — the CLI's own diagnosis is the useful part", err, want)
		}
	}
}

func TestUpReportsAFailureWithNoResultAtAll(t *testing.T) {
	cli := fakeCLI(t, "", "docker daemon is not running", 1)

	_, err := cli.Up(context.Background(), "/w")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "docker daemon") {
		t.Errorf("error = %q, want it to surface stderr when there is no JSON to read", err)
	}
}

// TestUpRejectsSuccessWithoutAContainerID: "success" with nothing to connect to
// would otherwise become a confusing failure one step later, against an empty
// container id.
func TestUpRejectsSuccessWithoutAContainerID(t *testing.T) {
	cli := fakeCLI(t, `{"outcome":"success","remoteWorkspaceFolder":"/w"}`, "", 0)

	if _, err := cli.Up(context.Background(), "/w"); err == nil {
		t.Fatal("expected an error for success with no container id")
	}
}

func TestReadConfigurationFindsIndigoCustomizations(t *testing.T) {
	out := `{"configuration":{"image":"x","customizations":{"vscode":{"extensions":[]},"indigo":{"serverPath":"/usr/local/bin/indigo-server"}}}}`
	cli := fakeCLI(t, out, "", 0)

	cfg, err := cli.ReadConfiguration(context.Background(), "/w")
	if err != nil {
		t.Fatalf("ReadConfiguration: %v", err)
	}
	if got := cfg.Customizations.Indigo.ServerPath; got != "/usr/local/bin/indigo-server" {
		t.Errorf("serverPath = %q, want the project's", got)
	}
}

// TestReadConfigurationToleratesAnAbsentBlock: customizations.indigo is
// optional, and a project that has never heard of indigo must not be an error.
func TestReadConfigurationToleratesAnAbsentBlock(t *testing.T) {
	cli := fakeCLI(t, `{"configuration":{"image":"x","customizations":{"vscode":{}}}}`, "", 0)

	cfg, err := cli.ReadConfiguration(context.Background(), "/w")
	if err != nil {
		t.Fatalf("ReadConfiguration: %v", err)
	}
	if cfg.Customizations.Indigo.ServerPath != "" {
		t.Errorf("serverPath = %q, want empty", cfg.Customizations.Indigo.ServerPath)
	}
}

func TestMissingCLIIsReportedWithHowToInstallIt(t *testing.T) {
	cli := CLI{Command: "definitely-not-a-real-devcontainer-cli"}
	if cli.Available() {
		t.Skip("a command by that name exists here")
	}
	_, err := cli.Up(context.Background(), "/w")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "install.sh") {
		t.Errorf("error = %q, want it to name the install script", err)
	}
	if !strings.Contains(err.Error(), "--container") {
		t.Errorf("error = %q, want it to mention the attach alternative", err)
	}
}

// TestAttachHonoursAServerPathFromTheProject: an image that already ships a
// server should not have one copied over it.
func TestAttachHonoursAServerPathFromTheProject(t *testing.T) {
	rt := &fakeRuntime{arch: "arm64", exists: true}

	stream, err := Attach(context.Background(), rt, "c1", "/w", AttachOptions{
		ServerPath: "/usr/local/bin/indigo-server",
		Locate: func(string) (string, error) {
			t.Error("looked for a local binary although the project supplied a path")
			return "", nil
		},
	})
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	defer stream.Close() //nolint:errcheck

	if rt.execArgv[0] != "/usr/local/bin/indigo-server" {
		t.Errorf("started %q, want the project's server", rt.execArgv[0])
	}
}

func TestExecArgsPassTheRemoteUser(t *testing.T) {
	args := execArgs("c1", "vscode", []string{"/srv", "/w"})
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-u vscode") {
		t.Errorf("args = %v, want -u vscode; a server running as root writes files the developer cannot edit", args)
	}
	// Still no TTY, and the user flag must sit before the container id.
	for _, a := range args {
		if a == "-t" || a == "-it" || a == "-ti" {
			t.Fatalf("execArgs allocated a TTY: %v", args)
		}
	}
	if indexOf(args, "-u") > indexOf(args, "c1") {
		t.Errorf("args = %v, want -u before the container id", args)
	}
	// And no user means no flag at all, rather than an empty one.
	if contains(execArgs("c1", "", []string{"x"}), "-u") {
		t.Error("an empty user still produced a -u flag")
	}
}
