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
	// ShutdownAction is devcontainer.json's shutdownAction: "none",
	// "stopContainer" or "stopCompose".
	//
	// Empty when the file does not say — the CLI does not fill in a default,
	// verified against 0.89.0 — so applying the spec's default is the tool's
	// job. See ShouldStopOnExit.
	ShutdownAction string `json:"shutdownAction"`

	// DockerComposeFile is present when the dev container is compose-based, in
	// which case "stopping the container" means bringing a project down and
	// not stopping one container. Kept as raw JSON because the spec allows
	// either a string or an array and nothing here needs to read it — only to
	// know whether it is there.
	DockerComposeFile json.RawMessage `json:"dockerComposeFile"`

	// RemoteEnv is devcontainer.json's remoteEnv: environment for the tools
	// that run *in* the container on the editor's behalf, merged across the
	// file and its features (see readConfigOutput.remoteEnv). A nil value is
	// the spec's way of unsetting a variable. Values may still hold
	// ${containerEnv:...} references; see ResolveRemoteEnv.
	//
	// indigo reads this because it starts its server with a plain `docker
	// exec`, which gives it the image's ENV and nothing else — not what a login
	// shell adds, and not remoteEnv, which only the devcontainer CLI's own
	// exec applies. Without it a language server installed somewhere remoteEnv
	// puts on PATH was invisible to the server.
	RemoteEnv map[string]*string `json:"remoteEnv"`

	Customizations struct {
		Indigo IndigoCustomizations `json:"indigo"`
	} `json:"customizations"`
}

// IsCompose reports whether the dev container is docker-compose based.
func (c Configuration) IsCompose() bool {
	t := strings.TrimSpace(string(c.DockerComposeFile))
	return t != "" && t != "null" && t != `""` && t != "[]"
}

// ShouldStopOnExit reports whether the container should be stopped when the
// last window closes.
//
// The specification's default is stopContainer (or stopCompose for compose), so
// an absent value means *stop*. That is also what VS Code does, and it is the
// behaviour that does not quietly accumulate running containers on a laptop.
// Only "none" opts out.
func (c Configuration) ShouldStopOnExit() bool {
	return c.ShutdownAction != "none"
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

// readConfigOutput is what `devcontainer read-configuration
// --include-merged-configuration` prints.
//
// The two blocks carry the *same* customizations in **different shapes**, which
// is the sort of thing only reading real output tells you:
//
//	"configuration":       {"customizations": {"indigo":  {"serverPath": "..."}}}
//	"mergedConfiguration": {"customizations": {"indigo": [{"serverPath": "..."}]}}
//
// An object in one, an array in the other — because the merged view collects a
// contribution per source (the file, plus each feature). Modelling both as an
// object, as an earlier version did, makes the *whole line* fail to unmarshal,
// so nothing is read at all and serverPath silently never works.
type readConfigOutput struct {
	Configuration Configuration `json:"configuration"`
	Merged        struct {
		Customizations struct {
			Indigo []IndigoCustomizations `json:"indigo"`
		} `json:"customizations"`
		// RemoteEnv is raw because its merged shape is not pinned down here:
		// customizations taught that the merged view can turn an object into
		// an array of per-source contributions, and guessing wrong would fail
		// the whole line (see above). mergedRemoteEnv accepts either.
		RemoteEnv json.RawMessage `json:"remoteEnv"`
	} `json:"mergedConfiguration"`
}

// remoteEnv merges the environment from both views: features' contributions
// (in the merged view) first, then the project's own file on top, so what the
// project wrote wins — the same precedence indigoCustomizations uses.
func (o readConfigOutput) remoteEnv() map[string]*string {
	out := mergedRemoteEnv(o.Merged.RemoteEnv)
	for k, v := range o.Configuration.RemoteEnv {
		if out == nil {
			out = map[string]*string{}
		}
		out[k] = v
	}
	return out
}

// mergedRemoteEnv decodes mergedConfiguration.remoteEnv as either one object or
// an array of objects applied in order. Anything else is ignored rather than
// failing the read — remoteEnv is an addition to what worked without it.
func mergedRemoteEnv(raw json.RawMessage) map[string]*string {
	if len(raw) == 0 {
		return nil
	}
	var one map[string]*string
	if err := json.Unmarshal(raw, &one); err == nil {
		return one
	}
	var many []map[string]*string
	if err := json.Unmarshal(raw, &many); err != nil {
		return nil
	}
	var out map[string]*string
	for _, m := range many {
		for k, v := range m {
			if out == nil {
				out = map[string]*string{}
			}
			out[k] = v
		}
	}
	return out
}

// indigoCustomizations picks the settings to use.
//
// What the project wrote in its own devcontainer.json wins; the merged view is
// consulted only for what the file did not say, so a value contributed by a
// feature is still found. Preferring the file is the less surprising direction:
// it is the thing whose author can see it.
func (o readConfigOutput) indigoCustomizations() IndigoCustomizations {
	out := o.Configuration.Customizations.Indigo
	for _, c := range o.Merged.Customizations.Indigo {
		if out.ServerPath == "" {
			out.ServerPath = c.ServerPath
		}
	}
	return out
}

// ReadConfiguration returns the resolved configuration for workspaceFolder,
// with variable substitution and feature merging already applied by the CLI.
//
// A configuration that cannot be read is not fatal to the caller: it only means
// no indigo-specific customizations, and failing to start an editor over an
// optional settings block would be the wrong trade. It needs the container
// runtime too — it shells out to `docker ps` to find an existing container.
func (c CLI) ReadConfiguration(ctx context.Context, workspaceFolder string) (Configuration, error) {
	if !c.Available() {
		return Configuration{}, ErrCLIMissing
	}
	var stdout, stderr bytes.Buffer
	cfgArgs := []string{"read-configuration", "--workspace-folder", workspaceFolder,
		"--include-merged-configuration"}
	cmd := exec.CommandContext(ctx, c.bin(), cfgArgs...)
	if runtimePath, offPath, err := RuntimePath(); err == nil {
		if offPath {
			cfgArgs = append(cfgArgs, "--docker-path", runtimePath)
			cmd = exec.CommandContext(ctx, c.bin(), cfgArgs...)
		}
		cmd.Env = childEnv(runtimePath, offPath)
	}
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return Configuration{}, fmt.Errorf("devcontainer read-configuration: %w: %s", err, tail(stderr.String()))
	}
	// Verified against devcontainer CLI 0.89.0 — see readConfigOutput for the
	// shape, which is not what it looks like.
	//
	// The whole resolved configuration is kept, not just the indigo block. An
	// earlier version built a fresh Configuration holding only the
	// customizations, which silently discarded shutdownAction and
	// dockerComposeFile — so `"shutdownAction": "none"` was ignored and indigo
	// stopped a container the project had explicitly asked it to leave running.
	// The match likewise cannot be conditioned on the indigo block alone: a
	// file that sets only shutdownAction has to be read too.
	if o, ok := lastJSONObject[readConfigOutput](stdout.Bytes(), func(o readConfigOutput) bool {
		return o.indigoCustomizations() != IndigoCustomizations{} ||
			o.Configuration.ShutdownAction != "" ||
			o.Configuration.IsCompose() ||
			len(o.remoteEnv()) > 0
	}); ok {
		cfg := o.Configuration
		cfg.Customizations.Indigo = o.indigoCustomizations()
		cfg.RemoteEnv = o.remoteEnv()
		return cfg, nil
	}
	// Nothing for us in it, which is the normal case for a project that has
	// never heard of indigo. Also covers an older CLI printing the resolved
	// file bare, which parses into Configuration directly.
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
