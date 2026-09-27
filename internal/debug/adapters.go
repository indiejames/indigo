package debug

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/indiejames/indigo/internal/config"
	"github.com/indiejames/indigo/internal/dap"
)

// Adapters. Go is built in (Delve, which needs its own launch arguments and
// listens on TCP); every other language goes through a config.DebugAdapter —
// a command speaking DAP — with launch arguments that are mostly the
// adapter's own business, passed through from the configuration.

// SetAdapters replaces the non-Go adapters this manager can start. Until it is
// called, the built-in defaults are used.
func (m *Manager) SetAdapters(adapters []config.DebugAdapter) {
	m.mu.Lock()
	m.adapters = adapters
	m.mu.Unlock()
}

// adapter returns the configured adapter called name, or nil — always nil for
// Go, which is not configured this way.
func (m *Manager) adapter(name string) *config.DebugAdapter {
	if isGo(name) {
		return nil
	}
	m.mu.Lock()
	list := m.adapters
	m.mu.Unlock()
	if list == nil {
		list = config.DefaultDebugAdapters
	}
	for i := range list {
		if list[i].Name == name {
			a := list[i]
			return &a
		}
	}
	return nil
}

func isGo(adapter string) bool { return adapter == "go" || adapter == "" }

// adapterID is what the initialize request names the adapter as.
func adapterID(cfg Config, a *config.DebugAdapter) string {
	if a == nil {
		return cfg.Adapter
	}
	if a.AdapterID != "" {
		return a.AdapterID
	}
	return a.Name
}

// chooseAdapter fills in the adapter for a configuration that names none: a
// source file is debugged by the adapter that claims its extension, and
// anything else — a Go file, a package directory — by Delve.
func (m *Manager) chooseAdapter(cfg Config) (Config, error) {
	if cfg.Adapter != "" {
		return cfg, nil
	}
	cfg.Adapter = "go"
	ext := strings.ToLower(filepath.Ext(cfg.Program))
	if ext == "" || ext == ".go" {
		return cfg, nil
	}
	if info, err := os.Stat(cfg.Program); err == nil && info.IsDir() {
		return cfg, nil
	}
	m.mu.Lock()
	list := m.adapters
	m.mu.Unlock()
	if list == nil {
		list = config.DefaultDebugAdapters
	}
	for _, a := range list {
		for _, e := range a.Extensions {
			if strings.EqualFold(strings.TrimPrefix(e, "."), ext[1:]) {
				cfg.Adapter = a.Name
				return cfg, nil
			}
		}
	}
	return cfg, fmt.Errorf("no debugger for %s files: add a [[debug_adapter]] with extensions = [%q]", ext, ext)
}

// launchArgs is the launch request's arguments. a is the configured adapter,
// nil for Go. cfg.Launch is merged last, so a configuration can override
// anything indigo sets.
func launchArgs(cfg Config, a *config.DebugAdapter) (map[string]any, error) {
	env, err := envMap(cfg.Env)
	if err != nil {
		return nil, err
	}
	var args map[string]any
	switch {
	case isGo(cfg.Adapter):
		mode := cfg.Mode
		if mode == "" {
			mode = "debug"
		}
		args = map[string]any{
			"mode": mode,
			// Symlinks resolved: Delve matches breakpoints against the path the
			// compiler recorded — the build directory as spelled when the build
			// ran — and indigo's paths are symlink-resolved (cmd/indigo's
			// resolvePath). Launching through /var and setting breakpoints on
			// /private/var fails with "could not find file". Found against
			// real Delve.
			"program": resolve(cfg.Program),
			// The program's stdout/stderr as output events; without it Delve
			// writes them to its own stdout and no window ever sees them.
			"outputMode": "remote",
		}
		if cfg.BuildFlags != "" {
			args["buildFlags"] = cfg.BuildFlags
		}
	case a != nil:
		args = map[string]any{}
		for k, v := range a.Launch {
			args[k] = v
		}
		if cfg.Program != "" {
			args["program"] = resolve(cfg.Program)
		}
	default:
		return nil, fmt.Errorf("no debugger named %q: add a [[debug_adapter]] for it, or use \"go\"", cfg.Adapter)
	}
	args["request"] = "launch"
	if cfg.Cwd != "" {
		args["cwd"] = resolve(cfg.Cwd)
	}
	if len(cfg.Args) > 0 {
		args["args"] = cfg.Args
	}
	if env != nil {
		args["env"] = env
	}
	for k, v := range cfg.Launch {
		args[k] = v
	}
	return args, nil
}

func envMap(kvs []string) (map[string]string, error) {
	if len(kvs) == 0 {
		return nil, nil
	}
	env := map[string]string{}
	for _, kv := range kvs {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("environment entry %q is not KEY=value", kv)
		}
		env[k] = v
	}
	return env, nil
}

// startDefaultAdapter starts the debugger cfg names.
func (m *Manager) startDefaultAdapter(ctx context.Context, cfg Config, handler func(dap.Event)) (*dap.Client, error) {
	dir := resolve(cfg.Cwd)
	if dir == "" {
		dir = resolve(cfg.Program)
		if info, err := os.Stat(dir); err == nil && !info.IsDir() {
			dir = filepath.Dir(dir)
		}
	}
	if isGo(cfg.Adapter) {
		dlv, err := findDelve()
		if err != nil {
			return nil, err
		}
		return dap.StartListening(ctx, dlv, []string{"dap", "--listen=127.0.0.1:0"}, dir, nil, handler)
	}
	a := m.adapter(cfg.Adapter)
	if a == nil {
		return nil, fmt.Errorf("no debugger named %q: add a [[debug_adapter]] for it, or use \"go\"", cfg.Adapter)
	}
	args := a.Args
	cmd, err := findCommand(a.Command)
	if err != nil && a.Command == jsDebugLauncher {
		cmd, args, err = findJSDebug(a.Args)
	}
	if err != nil {
		return nil, fmt.Errorf("the %s debugger: %w", a.Name, err)
	}
	switch a.Transport {
	case "", "stdio":
		return dap.StartStdio(cmd, args, dir, nil, handler)
	case "tcp":
		return dap.StartListening(ctx, cmd, args, dir, nil, handler)
	}
	return nil, fmt.Errorf("the %s debugger: unknown transport %q (stdio or tcp)", a.Name, a.Transport)
}

// findCommand locates an adapter's command on PATH, and on macOS also among
// Xcode's developer tools, where lldb-dap lives without being on PATH.
func findCommand(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("no command configured")
	}
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	if runtime.GOOS == "darwin" && !strings.ContainsRune(name, '/') {
		if out, err := exec.Command("xcrun", "-f", name).Output(); err == nil {
			if p := strings.TrimSpace(string(out)); p != "" {
				return p, nil
			}
		}
	}
	return "", fmt.Errorf("%s was not found on PATH", name)
}

// jsDebugLauncher is the command the built-in node adapter names.
const jsDebugLauncher = "js-debug-adapter"

// findJSDebug finds js-debug's standalone DAP server when no js-debug-adapter
// launcher is on PATH, and returns the command that runs it: node with the
// server script ahead of args. It looks where the release is usually
// unpacked — indigo's data directory and Mason's package directory.
func findJSDebug(args []string) (string, []string, error) {
	node, err := findCommand("node")
	if err != nil {
		return "", nil, fmt.Errorf("js-debug needs Node.js: %w", err)
	}
	var candidates []string
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		candidates = append(candidates, filepath.Join(d, "indigo", "js-debug"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates,
			filepath.Join(home, ".local", "share", "indigo", "js-debug"),
			filepath.Join(home, ".local", "share", "nvim", "mason", "packages", "js-debug-adapter", "js-debug"),
		)
	}
	for _, dir := range candidates {
		script := filepath.Join(dir, "src", "dapDebugServer.js")
		if info, err := os.Stat(script); err == nil && !info.IsDir() {
			return node, append([]string{script}, args...), nil
		}
	}
	return "", nil, fmt.Errorf("js-debug was not found: unpack its js-debug-dap release into ~/.local/share/indigo " +
		"(so that ~/.local/share/indigo/js-debug/src/dapDebugServer.js exists), or configure a [[debug_adapter]] named \"node\"")
}
