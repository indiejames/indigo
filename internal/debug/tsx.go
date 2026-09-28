package debug

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
)

// tsx and debuggers. tsx appends an inline source map — the only kind a
// debugger can see — only when Error.prepareStackTrace is not already a
// function; otherwise it keeps its maps in memory for source-map-support
// (tsx 4, dist/source-map.mjs). Node 22 and later define
// Error.prepareStackTrace themselves, so a tsx program exposes no maps, and a
// breakpoint in a .ts file has nothing to bind to — attached or launched,
// and for an ES module even when js-debug starts it, since tsx compiles
// modules in Node's loader thread, where Node's own function is in place.
//
// Deleting Error.prepareStackTrace before tsx loads puts tsx back on the path
// it was written to take. Stack traces stay mapped to the .ts files: Node
// maps them itself once tsx enables source maps (checked, tsx 4 on Node 23).
// Loaded through NODE_OPTIONS so it runs in the loader thread as well; a
// --require on the command line reaches only the main thread, which was
// checked too, and is not enough.

// tsxPreloadSource is the preload's content.
const tsxPreloadSource = `// Written by indigo. Lets a debugger see tsx's source maps on Node 22+:
// tsx inlines them only when Error.prepareStackTrace is not a function, and
// newer Node defines one. Load it with NODE_OPTIONS="--require <this file>".
delete Error.prepareStackTrace;
`

// TSXPreloadPath returns the preload file, written if it is missing or out of
// date: ~/.local/share/indigo/tsx-sourcemaps.cjs, beside js-debug.
func TSXPreloadPath() (string, error) {
	dir := os.Getenv("XDG_DATA_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".local", "share")
	}
	path := filepath.Join(dir, "indigo", "tsx-sourcemaps.cjs")
	if cur, err := os.ReadFile(path); err == nil && bytes.Equal(cur, []byte(tsxPreloadSource)) {
		return path, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	return path, os.WriteFile(path, []byte(tsxPreloadSource), 0o644)
}

// isTSXRuntime reports whether a launch's runtimeExecutable is tsx.
func isTSXRuntime(v any) bool {
	s, _ := v.(string)
	return s != "" && strings.TrimSuffix(filepath.Base(s), ".cmd") == "tsx"
}

// withTSXPreload adds the preload to cfg's NODE_OPTIONS for a launch through
// tsx, keeping any NODE_OPTIONS the configuration set. cfg is returned
// unchanged when it is not a tsx launch or the file cannot be written.
func withTSXPreload(cfg Config) Config {
	if cfg.attaching() || !isTSXRuntime(cfg.Launch["runtimeExecutable"]) {
		return cfg
	}
	path, err := TSXPreloadPath()
	if err != nil {
		return cfg
	}
	req := "--require " + path
	env := make([]string, 0, len(cfg.Env)+1)
	found := false
	for _, kv := range cfg.Env {
		if v, ok := strings.CutPrefix(kv, "NODE_OPTIONS="); ok {
			found = true
			if !strings.Contains(v, path) {
				kv = "NODE_OPTIONS=" + strings.TrimSpace(v+" "+req)
			}
		}
		env = append(env, kv)
	}
	if !found {
		env = append(env, "NODE_OPTIONS="+req)
	}
	cfg.Env = env
	return cfg
}
