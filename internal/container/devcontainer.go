package container

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// CLI is the `devcontainer` reference implementation of the dev container
// specification.
//
// # Why delegate
//
// The specification is large — docker-compose, features with install ordering,
// six lifecycle hooks plus waitFor, variable substitution, remoteUser and
// updateRemoteUserUID, port attributes, host requirements. `devcontainer up`
// hands back exactly the four facts needed to connect, and
// `read-configuration` resolves the variable substitution and feature merging
// that are the genuinely hard parts. A partial reimplementation that silently
// ignored half a config would be worse than not reading one at all, and VS
// Code, JetBrains Gateway and DevPod all consume the spec rather than
// reimplement it.
//
// The CLI installs with a script that bundles its own Node, so delegating does
// not push a Node toolchain onto anyone.
//
// # What is not delegated
//
// The data channel. Lifecycle and config resolution go through the CLI; the
// capnp stream is opened against the container runtime directly, using the
// containerId and remoteUser returned here — otherwise a Node process would
// relay every byte of every keystroke for the life of the session.
type CLI struct {
	Command string
}

// bin resolves the CLI, looking beyond PATH.
//
// The official install script puts it in ~/.devcontainers/bin and does *not*
// add that to PATH — verified against a real 0.89.0 install, where the CLI was
// present and working and `exec.LookPath` still could not find it. Relying on
// PATH alone means telling someone to install a thing they have already
// installed, which is the most annoying possible error message.
func (c CLI) bin() string {
	if c.Command != "" {
		return c.Command
	}
	if p, err := exec.LookPath("devcontainer"); err == nil {
		return p
	}
	for _, p := range defaultCLIPaths() {
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p
		}
	}
	return "devcontainer"
}

// defaultCLIPaths are the install locations to check when PATH does not have
// it. The first is where the official install script puts it.
func defaultCLIPaths() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return []string{
		filepath.Join(home, ".devcontainers", "bin", "devcontainer"),
		filepath.Join(home, ".local", "bin", "devcontainer"),
	}
}

// Available reports whether the CLI can be found.
func (c CLI) Available() bool {
	bin := c.bin()
	if filepath.IsAbs(bin) {
		info, err := os.Stat(bin)
		return err == nil && !info.IsDir()
	}
	_, err := exec.LookPath(bin)
	return err == nil
}

// ErrCLIMissing is returned when the devcontainer CLI is not installed. The
// message names the install script, because "not found" on its own leaves
// someone searching for which of several things to install.
var ErrCLIMissing = errors.New(
	"the devcontainer CLI is not installed — install it with:\n" +
		"  curl -fsSL https://raw.githubusercontent.com/devcontainers/cli/main/scripts/install.sh | sh\n" +
		"(it bundles its own Node runtime), or attach to an already-running container with --container")

// UpResult is what `devcontainer up` reports on success.
type UpResult struct {
	Outcome               string `json:"outcome"`
	ContainerID           string `json:"containerId"`
	RemoteUser            string `json:"remoteUser"`
	RemoteWorkspaceFolder string `json:"remoteWorkspaceFolder"`

	// Present when outcome is not "success".
	Message     string `json:"message"`
	Description string `json:"description"`
}

// Up builds and starts the dev container for workspaceFolder.
//
// This is the slow call in the whole feature: a cold image can mean a pull, a
// build and every lifecycle hook, so the caller's context wants a generous
// deadline rather than a polite one.
func (c CLI) Up(ctx context.Context, workspaceFolder string) (UpResult, error) {
	if !c.Available() {
		return UpResult{}, ErrCLIMissing
	}
	// Checked before the CLI is invoked, not after. Without a runtime the CLI
	// fails with "spawn docker ENOENT", which is Node for "not installed" and
	// reads like an internal fault — verified against a real 0.89.0. Saying
	// what is actually missing is worth one LookPath.
	args := []string{"up", "--workspace-folder", workspaceFolder}
	runtimePath, offPath, err := RuntimePath()
	if err != nil {
		return UpResult{}, err
	}
	if offPath {
		// The CLI spawns the runtime itself and inherits our PATH, so one we
		// found off-PATH has to be handed over explicitly.
		args = append(args, "--docker-path", runtimePath)
	}

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, c.bin(), args...)
	cmd.Env = childEnv(runtimePath, offPath)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()

	res, found := lastJSONObject[UpResult](stdout.Bytes(), func(r UpResult) bool { return r.Outcome != "" })
	if !found {
		if runErr != nil {
			return UpResult{}, fmt.Errorf("devcontainer up: %w: %s", runErr, tail(stderr.String()))
		}
		return UpResult{}, fmt.Errorf("devcontainer up produced no result: %s", tail(stderr.String()))
	}
	if res.Outcome != "success" {
		return res, fmt.Errorf("devcontainer up: %s: %s %s",
			res.Outcome, res.Message, res.Description)
	}
	if res.ContainerID == "" {
		return res, errors.New("devcontainer up reported success with no container id")
	}
	return res, nil
}

// Configuration is the part of a resolved devcontainer.json indigo reads.
//
// Deliberately a narrow view. Everything else in the file is the CLI's
// business, and modelling fields nothing here consumes would invite them to
// drift out of date silently.
type Configuration struct {
	Customizations struct {
		Indigo IndigoCustomizations `json:"indigo"`
	} `json:"customizations"`
}

// IndigoCustomizations is `customizations.indigo` in devcontainer.json.
//
// customizations is a documented open namespace — VS Code uses
// customizations.vscode, Codespaces customizations.codespaces — so a key here
// is a legitimate way for a project to configure the editor, not an extension
// of the spec.
type IndigoCustomizations struct {
	// ServerPath is an indigo server already present in the image. Set it and
	// indigo runs that instead of copying one in, which is what an image that
	// bakes indigo into itself wants.
	ServerPath string `json:"serverPath"`
}

// ReadConfiguration returns the resolved configuration for workspaceFolder,
// with variable substitution and feature merging already applied by the CLI.
//
// A configuration that cannot be read is not fatal to the caller: it only means
// no indigo-specific customizations, and failing to start an editor over an
// optional settings block would be the wrong trade.
func (c CLI) ReadConfiguration(ctx context.Context, workspaceFolder string) (Configuration, error) {
	if !c.Available() {
		return Configuration{}, ErrCLIMissing
	}
	var stdout, stderr bytes.Buffer
	// --include-merged-configuration so customizations contributed by a
	// *feature* are merged in, not only those written directly in
	// devcontainer.json. It adds a key rather than changing the existing one,
	// and the parsing below accepts either shape.
	cfgArgs := []string{"read-configuration", "--workspace-folder", workspaceFolder,
		"--include-merged-configuration"}
	if runtimePath, offPath, err := RuntimePath(); err == nil && offPath {
		// read-configuration shells out to `docker ps` to find an existing
		// container, so it needs the runtime too — confirmed by watching it
		// fail with exactly that call.
		cfgArgs = append(cfgArgs, "--docker-path", runtimePath)
	}
	cmd := exec.CommandContext(ctx, c.bin(), cfgArgs...)
	if runtimePath, offPath, err := RuntimePath(); err == nil {
		cmd.Env = childEnv(runtimePath, offPath)
	}
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return Configuration{}, fmt.Errorf("devcontainer read-configuration: %w: %s", err, tail(stderr.String()))
	}
	// Three shapes are accepted: the merged configuration, the resolved file
	// under a "configuration" key, and — for older versions — the file bare.
	// Leniency rather than pinning a version, because the whole block is
	// optional and an unreadable one is indistinguishable from an absent one.
	//
	// Untested against a real CLI: read-configuration shells out to `docker ps`
	// and so needs a runtime, which the machine this was written on does not
	// have. The `up` half above *is* verified against a real 0.89.0.
	type wrapper struct {
		Merged        Configuration `json:"mergedConfiguration"`
		Configuration Configuration `json:"configuration"`
	}
	if w, ok := lastJSONObject[wrapper](stdout.Bytes(), func(w wrapper) bool {
		return w.Merged.Customizations.Indigo != IndigoCustomizations{}
	}); ok {
		return w.Merged, nil
	}
	if w, ok := lastJSONObject[wrapper](stdout.Bytes(), func(w wrapper) bool {
		return w.Configuration.Customizations.Indigo != IndigoCustomizations{}
	}); ok {
		return w.Configuration, nil
	}
	cfg, _ := lastJSONObject[Configuration](stdout.Bytes(), func(Configuration) bool { return true })
	return cfg, nil
}

// lastJSONObject scans output line by line and returns the last line that
// parses into T and satisfies want.
//
// Line-wise rather than decoding the whole stream because the CLI interleaves
// progress records with its result, and which stream each lands on has changed
// between versions. Taking the last match is what makes this robust to that
// without pinning a version.
func lastJSONObject[T any](out []byte, want func(T) bool) (T, bool) {
	var found T
	var ok bool
	for _, line := range bytes.Split(out, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var v T
		if err := json.Unmarshal(line, &v); err != nil {
			continue
		}
		if want(v) {
			found, ok = v, true
		}
	}
	return found, ok
}

// tail keeps the end of a command's stderr for an error message: the useful
// part of a failed build is the last few lines, and the whole of it would bury
// the error that quoted it.
func tail(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > 8 {
		lines = lines[len(lines)-8:]
	}
	return strings.Join(lines, "\n")
}
