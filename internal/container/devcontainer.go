package container

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
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

func (c CLI) bin() string {
	if c.Command != "" {
		return c.Command
	}
	return "devcontainer"
}

// Available reports whether the CLI can be found.
func (c CLI) Available() bool {
	_, err := exec.LookPath(c.bin())
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
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, c.bin(), "up", "--workspace-folder", workspaceFolder)
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
	cmd := exec.CommandContext(ctx, c.bin(), "read-configuration", "--workspace-folder", workspaceFolder)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return Configuration{}, fmt.Errorf("devcontainer read-configuration: %w: %s", err, tail(stderr.String()))
	}
	// The CLI wraps the resolved file in a "configuration" key; older versions
	// print it bare. Both are accepted rather than pinning a version, since a
	// missing customizations block is indistinguishable from an absent one and
	// costs nothing to be lenient about.
	type wrapper struct {
		Configuration Configuration `json:"configuration"`
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
