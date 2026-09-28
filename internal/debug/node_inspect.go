package debug

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/indiejames/indigo/internal/config"
)

// Attaching to a Node program by process id. js-debug's standalone server
// attaches only by inspector port — the "attach to process" in VS Code is the
// editor extension switching the program's inspector on and then attaching by
// port — so indigo does that part: SIGUSR1 makes Node open its inspector on
// 127.0.0.1:9229, and the session attaches there.

// nodeInspectorAddr is where SIGUSR1 opens a Node inspector: Node's default.
// A variable so tests can use a port of their own (a program started with
// --inspect-port=N opens it there instead) rather than the shared 9229.
var nodeInspectorAddr = "127.0.0.1:9229"

// isJSDebug reports whether a is js-debug (the built-in node adapter, or a
// user's replacement for it).
func isJSDebug(a *config.DebugAdapter) bool {
	return a != nil && (a.AdapterID == "pwa-node" || a.Name == "node")
}

// attachNodeByPID prepares cfg, an attach to a Node process by id, to attach
// by inspector port instead: the program's inspector is switched on if it is
// not already, and cfg comes back with launch.port set and no process id.
//
// Port 9229 already in use is the careful case. It is this program's own
// inspector when a session attached to it before (a restart, a second
// attach), and another program's otherwise — attaching there would debug the
// wrong program. The inspector says which script it is running; that is
// checked against the process's arguments.
func attachNodeByPID(cfg Config) (Config, error) {
	pid := cfg.ProcessID
	if inspectorListening() {
		ok, err := inspectorIsProcess(pid)
		if err != nil {
			return cfg, err
		}
		if !ok {
			return cfg, fmt.Errorf("another Node program's inspector is already on %s; start this one with --inspect=<port> and attach by port", nodeInspectorAddr)
		}
	} else {
		// SIGUSR1's default action is to terminate: sent to anything but a
		// Node process it would kill the program instead of attaching to it.
		if !isNodeProcess(pid) {
			return cfg, fmt.Errorf("process %d is not a Node program (or could not be checked); not signalling it", pid)
		}
		if err := signalInspector(pid); err != nil {
			return cfg, fmt.Errorf("could not switch on process %d's inspector: %w", pid, err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for !inspectorListening() {
			if time.Now().After(deadline) {
				return cfg, fmt.Errorf("process %d did not open an inspector — is it a Node program?", pid)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	launch := make(map[string]any, len(cfg.Launch)+2)
	for k, v := range cfg.Launch {
		launch[k] = v
	}
	host, port, _ := net.SplitHostPort(nodeInspectorAddr)
	launch["port"], _ = strconv.Atoi(port)
	launch["address"] = host
	delete(launch, "processId")
	cfg.Launch = launch
	cfg.ProcessID = 0
	return cfg, nil
}

func inspectorListening() bool {
	c, err := net.DialTimeout("tcp", nodeInspectorAddr, 300*time.Millisecond)
	if err != nil {
		return false
	}
	c.Close() //nolint:errcheck
	return true
}

// inspectorIsProcess reports whether the inspector on 9229 is running a script
// process pid was started with.
func inspectorIsProcess(pid int) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+nodeInspectorAddr+"/json/list", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("could not ask the inspector on %s what it is running: %w", nodeInspectorAddr, err)
	}
	defer resp.Body.Close() //nolint:errcheck
	var targets []struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&targets); err != nil {
		return false, err
	}
	// The raw table, not ListProcesses: this is one known process, and the
	// picker's filtering (the server's own descendants, say) does not apply.
	raw, err := listRawProcesses()
	if err != nil {
		return false, err
	}
	var args []string
	for _, p := range raw {
		if p.pid == pid {
			args = p.args
		}
	}
	for _, t := range targets {
		if scriptMatches(t.URL, args) {
			return true, nil
		}
	}
	return false, nil
}

// scriptMatches reports whether an inspector target's URL is the script one
// of args names. The match must start at a path separator, so an argument
// x.js does not match .../index.js; a relative argument (src/index.ts) and an
// absolute one both match the file:// URL of the same file.
func scriptMatches(url string, args []string) bool {
	for _, a := range args {
		if a == "" || strings.HasPrefix(a, "-") {
			continue
		}
		p := filepath.ToSlash(filepath.Clean(a))
		if !strings.HasPrefix(p, "/") {
			p = "/" + p
		}
		if strings.HasSuffix(url, p) {
			return true
		}
	}
	return false
}

// isNodeProcess reports whether pid is a Node.js process — node, node.exe, or
// Debian's nodejs — and false when that cannot be established.
func isNodeProcess(pid int) bool {
	raw, err := listRawProcesses()
	if err != nil {
		return false
	}
	for _, p := range raw {
		if p.pid == pid {
			switch p.name {
			case "node", "node.exe", "nodejs":
				return true
			}
			return false
		}
	}
	return false
}

// tsxWarning explains, for a Node process run through tsx, why breakpoints in
// its .ts files will not bind — or returns "" for any other process.
//
// tsx appends an inline source map, which a debugger can see, only when
// Error.prepareStackTrace is not already a function; otherwise it keeps its
// maps in memory for source-map-support, where no debugger can reach them.
// Node 22 and later define Error.prepareStackTrace themselves, so on them a
// tsx program exposes no maps at all (checked against tsx 4 on Node 23,
// dist/source-map.mjs): the running code is tsx's single-line output, and a
// breakpoint on line 8 of the .ts has nothing to bind to. Launching avoids it
// — js-debug's bootloader is in the process before tsx is.
func tsxWarning(pid int) string {
	raw, err := listRawProcesses()
	if err != nil {
		return ""
	}
	for _, p := range raw {
		if p.pid != pid {
			continue
		}
		for _, a := range p.args {
			if strings.Contains(filepath.ToSlash(a), "/tsx/") || filepath.Base(a) == "tsx" {
				if hasTSXPreload(processEnv(pid)) {
					return "" // started with the fix: its maps are visible
				}
				fix := `NODE_OPTIONS="--require ` + tsxPreloadHint() + `"`
				return "this program runs its TypeScript through tsx, which on this Node hides its source maps " +
					"from debuggers, so breakpoints in its .ts files cannot bind. Restart it with " + fix +
					" in its environment (e.g. " + fix + " tsx --inspect src/index.ts) and attach again, " +
					"or run it with node instead of tsx"
			}
		}
	}
	return ""
}

// tsxPreloadHint is the preload's path for the warning: written now, so the
// command the warning gives works as shown.
func tsxPreloadHint() string {
	if path, err := TSXPreloadPath(); err == nil {
		return path
	}
	return "~/.local/share/indigo/tsx-sourcemaps.cjs"
}

// hasTSXPreload reports whether an environment's NODE_OPTIONS loads indigo's
// tsx preload.
func hasTSXPreload(env []string) bool {
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "NODE_OPTIONS="); ok && strings.Contains(v, "tsx-sourcemaps.cjs") {
			return true
		}
	}
	return false
}
