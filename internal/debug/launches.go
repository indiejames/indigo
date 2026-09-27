package debug

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/indiejames/indigo/internal/config"
)

// ProjectLaunchFile is where a workspace keeps its own named debug
// configurations, relative to the workspace root. Same [[debug]] shape as
// config.toml.
const ProjectLaunchFile = ".indigo/debug.toml"

// Launches returns the named debug configurations for the workspace at root:
// the workspace's own file first, then its .vscode/launch.json, then the
// user's config.toml entries (global) — each list skipping names an earlier
// one already uses. Paths are resolved against root. activeFile is what
// launch.json's ${file} refers to; "" when no file is open.
//
// The workspace file is read on every call, so an edit to it shows up the next
// time the list is asked for without restarting anything. A file that cannot
// be read or parsed is reported in the error; the configurations that could be
// loaded are returned alongside it, so one broken file does not hide the rest.
func (m *Manager) Launches(root string, global []config.DebugLaunch, activeFile string) ([]Config, error) {
	var errs []error
	var project []config.DebugLaunch
	path := filepath.Join(root, ProjectLaunchFile)
	if data, err := os.ReadFile(path); err == nil {
		var f struct {
			Debug []config.DebugLaunch `toml:"debug"`
		}
		if _, err := toml.Decode(string(data), &f); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", ProjectLaunchFile, err))
		} else {
			project = f.Debug
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		errs = append(errs, fmt.Errorf("%s: %w", ProjectLaunchFile, err))
	}

	var out []Config
	seen := map[string]bool{}
	add := func(source string, list []config.DebugLaunch, shadowable bool) {
		for i, l := range list {
			name := strings.TrimSpace(l.Name)
			switch {
			case name == "":
				errs = append(errs, fmt.Errorf("%s: [[debug]] entry %d has no name", source, i+1))
				continue
			case seen[name] && !shadowable:
				errs = append(errs, fmt.Errorf("%s: two [[debug]] entries are named %q", source, name))
				continue
			case seen[name]:
				continue // the workspace's entry of the same name wins
			}
			seen[name] = true
			out = append(out, resolveLaunch(root, l))
		}
	}
	add(ProjectLaunchFile, project, false)
	fromVSCode, vsErrs := m.readLaunchJSON(root, activeFile)
	errs = append(errs, vsErrs...)
	for _, c := range fromVSCode {
		if !seen[c.Name] { // VS Code allows duplicate names; the first wins here
			seen[c.Name] = true
			out = append(out, c)
		}
	}
	// A global entry never shadows a workspace one, but two global entries
	// with one name are still a mistake worth reporting.
	globalSeen := map[string]bool{}
	for _, l := range global {
		n := strings.TrimSpace(l.Name)
		if n != "" && globalSeen[n] {
			errs = append(errs, fmt.Errorf("config.toml: two [[debug]] entries are named %q", n))
		}
		globalSeen[n] = true
	}
	add("config.toml", global, true)
	return out, errors.Join(errs...)
}

// resolveLaunch turns a configuration as written into one ready to start.
func resolveLaunch(root string, l config.DebugLaunch) Config {
	expand := func(p string) string {
		p = strings.ReplaceAll(p, "${workspaceFolder}", root)
		if p == "" || filepath.IsAbs(p) {
			return p
		}
		return filepath.Join(root, p)
	}
	cfg := Config{
		Name:       strings.TrimSpace(l.Name),
		Adapter:    l.Adapter,
		Mode:       l.Mode,
		Program:    expand(l.Program),
		Cwd:        expand(l.Cwd),
		BuildFlags: l.BuildFlags,
		Launch:     l.Launch,
	}
	// An adapter left out is chosen when the session starts, by the program's
	// file type (Manager.chooseAdapter): Go unless another adapter claims it.
	if cfg.Mode == "" {
		cfg.Mode = "debug"
	}
	if cfg.Program == "" {
		cfg.Program = root
	}
	for _, a := range l.Args {
		cfg.Args = append(cfg.Args, strings.ReplaceAll(a, "${workspaceFolder}", root))
	}
	keys := make([]string, 0, len(l.Env))
	for k := range l.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys) // stable, so the same file gives the same config
	for _, k := range keys {
		cfg.Env = append(cfg.Env, k+"="+l.Env[k])
	}
	return cfg
}
