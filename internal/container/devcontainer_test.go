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
	// A stand-in runtime, so these tests exercise output parsing rather than
	// the runtime check that now runs before it. The fake CLI never spawns it.
	stubRuntime := filepath.Join(dir, "docker")
	if err := os.WriteFile(stubRuntime, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("INDIGO_DOCKER", stubRuntime)
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

// realReadConfigOutput is captured verbatim from devcontainer CLI 0.89.0 with
// --include-merged-configuration.
//
// The two blocks carry the same customizations in different shapes — an object
// under "configuration", an array under "mergedConfiguration". An earlier
// version modelled both as objects, which made the whole line fail to
// unmarshal, so serverPath was silently never read. That passed against a
// hand-written fixture, because the fixture had the shape the code expected.
const realReadConfigOutput = `{"configuration": {"customizations": {"vscode": {"extensions": ["golang.go"]}, "indigo": {"serverPath": "/usr/local/bin/indigo-server"}}}, "mergedConfiguration": {"customizations": {"vscode": [{"extensions": ["golang.go"]}], "indigo": [{"serverPath": "/usr/local/bin/indigo-server"}]}}}`

func TestReadConfigurationParsesRealCLIOutput(t *testing.T) {
	cli := fakeCLI(t, realReadConfigOutput, "", 0)

	cfg, err := cli.ReadConfiguration(context.Background(), "/w")
	if err != nil {
		t.Fatalf("ReadConfiguration: %v", err)
	}
	if got := cfg.Customizations.Indigo.ServerPath; got != "/usr/local/bin/indigo-server" {
		t.Errorf("serverPath = %q, want the project's", got)
	}
}

// TestMergedCustomizationsFillGapsInTheFile: a value contributed by a feature
// appears only in the merged view, and must still be found.
func TestMergedCustomizationsFillGapsInTheFile(t *testing.T) {
	out := `{"configuration":{"customizations":{}},"mergedConfiguration":{"customizations":{"indigo":[{"serverPath":"/from/a/feature"}]}}}`
	cli := fakeCLI(t, out, "", 0)

	cfg, err := cli.ReadConfiguration(context.Background(), "/w")
	if err != nil {
		t.Fatalf("ReadConfiguration: %v", err)
	}
	if got := cfg.Customizations.Indigo.ServerPath; got != "/from/a/feature" {
		t.Errorf("serverPath = %q, want the feature's contribution", got)
	}
}

// TestTheFileWinsOverTheMergedView: what the project's own devcontainer.json
// says beats a feature's contribution, because it is the one whose author can
// see it.
func TestTheFileWinsOverTheMergedView(t *testing.T) {
	out := `{"configuration":{"customizations":{"indigo":{"serverPath":"/from/the/file"}}},` +
		`"mergedConfiguration":{"customizations":{"indigo":[{"serverPath":"/from/a/feature"}]}}}`
	cli := fakeCLI(t, out, "", 0)

	cfg, err := cli.ReadConfiguration(context.Background(), "/w")
	if err != nil {
		t.Fatalf("ReadConfiguration: %v", err)
	}
	if got := cfg.Customizations.Indigo.ServerPath; got != "/from/the/file" {
		t.Errorf("serverPath = %q, want the file's", got)
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
	args := execArgs("c1", "vscode", nil, []string{"/srv", "/w"})
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
	if contains(execArgs("c1", "", nil, []string{"x"}), "-u") {
		t.Error("an empty user still produced a -u flag")
	}
}

// TestUpParsesRealCLIOutput uses output captured verbatim from devcontainer CLI
// 0.89.0, rather than a fixture written from the documentation.
//
// It pins two things the invented fixtures only assumed: the result is a single
// line on *stdout* while progress and stack traces go to stderr, and a failure
// still produces a result object rather than only an exit code. Both were
// confirmed by running the real CLI; neither is stated anywhere in its docs.
func TestUpParsesRealCLIOutput(t *testing.T) {
	// Verbatim, from `devcontainer up --workspace-folder .` in a directory with
	// no devcontainer.json. stderr carried a Node stack trace, which this
	// deliberately reproduces because it is exactly what must not be mistaken
	// for the result.
	realStdout := `{"outcome":"error","message":"Dev container config (/private/tmp/dcprobe/.devcontainer/devcontainer.json) not found.","description":"Dev container config (/private/tmp/dcprobe/.devcontainer/devcontainer.json) not found."}`
	realStderr := strings.Join([]string{
		"[2026-09-21T20:56:57.599Z] @devcontainers/cli 0.89.0. Node.js v20.20.2. darwin 25.6.0 arm64.",
		"    at kW (/Users/x/.devcontainers/cli/0.89.0/package/dist/spec-node/devContainersSpecCLI.js:488:3976)",
		"    at async hI (/Users/x/.devcontainers/cli/0.89.0/package/dist/spec-node/devContainersSpecCLI.js:488:5808)",
	}, "\n")

	cli := fakeCLI(t, realStdout, realStderr, 1)
	_, err := cli.Up(context.Background(), "/w")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "devcontainer.json") {
		t.Errorf("error = %q, want the CLI's own diagnosis", err)
	}
	// The Node stack trace must not leak into the message as if it were the
	// explanation.
	if strings.Contains(err.Error(), "devContainersSpecCLI.js") {
		t.Errorf("error = %q, want the CLI's message rather than its stack trace", err)
	}
}

// TestCLIIsFoundOutsideThePath is a regression test for something a live check
// found: the official install script puts the CLI in ~/.devcontainers/bin and
// does not add it to PATH, so exec.LookPath alone reported "not installed" for
// a CLI that was installed and working.
func TestCLIIsFoundOutsideThePath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// An empty PATH, so only the install-location search can succeed.
	t.Setenv("PATH", t.TempDir())

	var cli CLI
	if cli.Available() {
		t.Fatal("reported available before anything was installed")
	}

	installed := filepath.Join(home, ".devcontainers", "bin", "devcontainer")
	if err := os.MkdirAll(filepath.Dir(installed), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(installed, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	if !cli.Available() {
		t.Error("did not find the CLI at the official install location")
	}
	if got := cli.bin(); got != installed {
		t.Errorf("bin() = %q, want %q", got, installed)
	}
}

// TestExplicitCommandWinsOverDiscovery keeps the test seam honest: a caller that
// names a command must get that one, not whatever discovery turns up.
func TestExplicitCommandWinsOverDiscovery(t *testing.T) {
	cli := CLI{Command: "/somewhere/else/devcontainer"}
	if got := cli.bin(); got != "/somewhere/else/devcontainer" {
		t.Errorf("bin() = %q, want the explicit command", got)
	}
}

// TestUpSaysWhatIsMissingWhenThereIsNoRuntime is the message a live run showed
// was needed: without a container engine the CLI reports "spawn docker ENOENT",
// which is Node for "not installed" and reads like an internal fault. Confirmed
// against a real 0.89.0 before this check existed.
func TestUpSaysWhatIsMissingWhenThereIsNoRuntime(t *testing.T) {
	cli := fakeCLI(t, `{"outcome":"success","containerId":"abc"}`, "", 0)
	noRuntime(t)

	_, err := cli.Up(context.Background(), "/w")
	if err == nil {
		t.Fatal("expected an error with no runtime available")
	}
	if !strings.Contains(err.Error(), "container runtime") {
		t.Errorf("error = %q, want it to name what is missing", err)
	}
	if strings.Contains(err.Error(), "ENOENT") {
		t.Errorf("error = %q, want indigo's diagnosis rather than the CLI's", err)
	}
}

// TestRuntimeOverrideIsHonoured: discovery cannot cover every installer, and
// without an override a miss leaves the feature unusable with no way out.
func TestRuntimeOverrideIsHonoured(t *testing.T) {
	noRuntime(t)
	t.Setenv("INDIGO_DOCKER", "/my/own/docker")

	path, offPath, err := RuntimePath()
	if err != nil {
		t.Fatalf("RuntimePath: %v", err)
	}
	if path != "/my/own/docker" || !offPath {
		t.Errorf("RuntimePath = (%q, %v), want the override, flagged as off-PATH", path, offPath)
	}
	// Docker.bin must use it too, or --container mode fails opaquely against an
	// off-PATH install.
	if got := (Docker{}).bin(); got != "/my/own/docker" {
		t.Errorf("Docker.bin() = %q, want the override", got)
	}
}

// noRuntime makes discovery find nothing, whatever is installed on the machine
// running the test. PATH keeps /bin and /usr/bin so shell fixtures still work;
// no container runtime installs itself there.
func noRuntime(t *testing.T) {
	t.Helper()
	t.Setenv("INDIGO_DOCKER", "")
	t.Setenv("PATH", "/bin:/usr/bin")
	t.Setenv("HOME", t.TempDir())
	prev := runtimeCandidates
	runtimeCandidates = func() []string { return nil }
	t.Cleanup(func() { runtimeCandidates = prev })
}
