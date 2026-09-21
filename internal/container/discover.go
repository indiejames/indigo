package container

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// FindRuntime locates the container engine.
//
// PATH first, then the places macOS installers put it without touching PATH —
// the same problem the devcontainer CLI itself has, and worth solving the same
// way. Docker Desktop, Colima, OrbStack and Podman all end up somewhere a
// shell that was opened before the install cannot see.
//
// Returns the path and whether it came from somewhere PATH would not have
// found, which is what decides whether the devcontainer CLI needs telling.
func FindRuntime() (path string, offPath bool, err error) {
	for _, name := range []string{"docker", "podman"} {
		if p, lookErr := exec.LookPath(name); lookErr == nil {
			return p, false, nil
		}
	}
	for _, p := range runtimeCandidates() {
		if info, statErr := os.Stat(p); statErr == nil && !info.IsDir() {
			return p, true, nil
		}
	}
	return "", false, ErrNoRuntime
}

// ErrNoRuntime says the thing the CLI's own message does not: that what is
// missing is a container engine, and which ones would do.
//
// The CLI reports this as "spawn docker ENOENT", which is Node for "not
// installed" and reads like an internal fault. Worth intercepting, because it
// is the first thing anyone trying this feature will hit.
var ErrNoRuntime = errors.New(
	"no container runtime found — indigo needs Docker, OrbStack, Colima or Podman " +
		"to run a dev container.\nIf one is installed but not on your PATH, " +
		"put it there or set INDIGO_DOCKER to its full path")

// runtimeCandidates is a var so a test can make discovery deterministic. It has
// to be: the candidates are absolute paths outside any temp directory, so
// whether a test finds a runtime otherwise depends on what is installed on the
// machine running it — which is how a "no runtime available" test passed for as
// long as nobody had Docker, and failed the moment somebody did.
var runtimeCandidates = defaultRuntimeCandidates

func defaultRuntimeCandidates() []string {
	paths := []string{
		"/Applications/Docker.app/Contents/Resources/bin/docker",
		"/usr/local/bin/docker",
		"/opt/homebrew/bin/docker",
		"/Applications/OrbStack.app/Contents/MacOS/bin/docker",
		"/opt/podman/bin/podman",
		"/usr/local/bin/podman",
		"/opt/homebrew/bin/podman",
	}
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths,
			filepath.Join(home, ".docker", "bin", "docker"),
			filepath.Join(home, ".orbstack", "bin", "docker"),
			filepath.Join(home, ".colima", "default", "docker.sock"), // not a binary; see below
		)
	}
	out := paths[:0]
	for _, p := range paths {
		if filepath.Ext(p) == ".sock" {
			continue // a socket is not something to exec
		}
		out = append(out, p)
	}
	return out
}

// RuntimePath returns the runtime to use, honouring an explicit override.
//
// INDIGO_DOCKER exists because discovery cannot cover every installer, and the
// alternative when it misses is a feature that simply does not work with no way
// for the user to fix it.
func RuntimePath() (string, bool, error) {
	if override := os.Getenv("INDIGO_DOCKER"); override != "" {
		return override, true, nil
	}
	return FindRuntime()
}

// childEnv returns an environment for a subprocess that will spawn the runtime,
// with the runtime's own directory on PATH.
//
// Docker Desktop keeps its credential helpers beside the docker binary —
// docker-credential-desktop, docker-credential-osxkeychain — and docker finds
// them through PATH, not relative to itself. So an off-PATH docker inherits our
// PATH, fails to find its helper, and any registry pull dies with
//
//	error getting credentials - err: exec: "docker-credential-desktop":
//	executable file not found in $PATH
//
// which names a binary the user has never heard of. Verified against a real
// Docker Desktop install, where this is the *default* state: the installer puts
// docker in ~/.docker/bin and does not touch PATH.
//
// The same applies to the devcontainer CLI, which spawns docker itself.
func childEnv(runtimePath string, offPath bool) []string {
	env := os.Environ()
	if !offPath || runtimePath == "" {
		return env
	}
	dir := filepath.Dir(runtimePath)
	for i, kv := range env {
		if name, val, ok := strings.Cut(kv, "="); ok && name == "PATH" {
			env[i] = "PATH=" + dir + string(os.PathListSeparator) + val
			return env
		}
	}
	return append(env, "PATH="+dir)
}
