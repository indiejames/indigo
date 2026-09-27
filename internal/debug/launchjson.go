package debug

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// VS Code's .vscode/launch.json, read so a project that already has one needs
// no second copy. Only "launch" requests are taken, and only for debuggers
// indigo has: Go ("go"), and any adapter whose name matches the entry's type
// once VS Code's type names are translated (debugpy → python, lldb-dap →
// lldb, node/pwa-node → node). Other entries — attach requests, compound configurations, Node — are
// skipped without complaint: a launch.json usually holds entries for tools
// that are not indigo's business.

// VSCodeLaunchFile is launch.json's place, relative to the workspace root.
const VSCodeLaunchFile = ".vscode/launch.json"

// vscodeTypes maps VS Code debug types to indigo adapter names.
var vscodeTypes = map[string]string{
	"go":       "go",
	"debugpy":  "python",
	"python":   "python",
	"lldb-dap": "lldb",
	"lldb":     "lldb",
	"node":     "node",
	"pwa-node": "node",
}

// launchJSONKeysDropped are VS Code settings with no meaning outside it —
// editor UI, task hooks — or that would break the launch here: debugpy refuses
// "console": "integratedTerminal" outright from a client without
// supportsRunInTerminalRequest (checked against debugpy 1.8), and falls back
// to its own console, which is what indigo wants, when it is left out.
var launchJSONKeysDropped = map[string]bool{
	"type": true, "request": true, "name": true, "program": true, "args": true,
	"env": true, "cwd": true, "mode": true, "buildFlags": true,
	"console": true, "presentation": true, "preLaunchTask": true, "postDebugTask": true,
	"internalConsoleOptions": true, "serverReadyAction": true, "envFile": true,
}

// readLaunchJSON returns the usable configurations in root's launch.json. A
// missing file is not an error; entries that cannot be used as written are
// reported, and the rest still returned.
func (m *Manager) readLaunchJSON(root, activeFile string) ([]Config, []error) {
	data, err := os.ReadFile(filepath.Join(root, VSCodeLaunchFile))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, []error{fmt.Errorf("%s: %w", VSCodeLaunchFile, err)}
	}
	var f struct {
		Configurations []map[string]any `json:"configurations"`
	}
	if err := json.Unmarshal(stripJSONC(data), &f); err != nil {
		return nil, []error{fmt.Errorf("%s: %w", VSCodeLaunchFile, err)}
	}
	var out []Config
	var errs []error
	for i, e := range f.Configurations {
		cfg, ok, err := m.fromLaunchJSON(root, activeFile, e)
		if err != nil {
			name, _ := e["name"].(string)
			if name == "" {
				name = fmt.Sprintf("entry %d", i+1)
			}
			errs = append(errs, fmt.Errorf("%s: %q: %w", VSCodeLaunchFile, name, err))
			continue
		}
		if ok {
			out = append(out, cfg)
		}
	}
	return out, errs
}

// fromLaunchJSON converts one entry. ok is false for an entry that is not for
// indigo (another tool's type, an attach request).
func (m *Manager) fromLaunchJSON(root, activeFile string, e map[string]any) (cfg Config, ok bool, err error) {
	typ, _ := e["type"].(string)
	adapter, known := vscodeTypes[typ]
	if !known {
		if m.adapter(typ) == nil {
			return Config{}, false, nil
		}
		adapter = typ
	}
	if req, _ := e["request"].(string); req != "launch" {
		return Config{}, false, nil
	}
	x := &varExpander{root: root, file: activeFile}
	str := func(key string) string {
		s, _ := e[key].(string)
		return x.expand(s)
	}
	cfg = Config{Adapter: adapter, Program: str("program"), Cwd: str("cwd")}
	cfg.Name, _ = e["name"].(string)
	if cfg.Name == "" {
		return Config{}, false, fmt.Errorf("no name")
	}
	if list, ok := e["args"].([]any); ok {
		for _, a := range list {
			if s, ok := a.(string); ok {
				cfg.Args = append(cfg.Args, x.expand(s))
			}
		}
	}
	if env, ok := e["env"].(map[string]any); ok {
		for k, v := range env {
			if s, ok := v.(string); ok {
				cfg.Env = append(cfg.Env, k+"="+x.expand(s))
			}
		}
		sort.Strings(cfg.Env)
	}
	if adapter == "go" {
		cfg.Mode = str("mode")
		switch cfg.Mode {
		case "", "auto":
			// vscode-go's "auto": a test file debugs its tests.
			cfg.Mode = "debug"
			if strings.HasSuffix(cfg.Program, "_test.go") {
				cfg.Mode = "test"
			}
		case "debug", "test":
		default:
			return Config{}, false, fmt.Errorf("mode %q is not supported (debug, test or auto)", cfg.Mode)
		}
		if cfg.Mode == "test" && strings.HasSuffix(cfg.Program, ".go") {
			cfg.Program = filepath.Dir(cfg.Program) // go test runs a package, not a file
		}
		switch bf := e["buildFlags"].(type) {
		case string:
			cfg.BuildFlags = x.expand(bf)
		case []any:
			var parts []string
			for _, p := range bf {
				if s, ok := p.(string); ok {
					parts = append(parts, x.expand(s))
				}
			}
			cfg.BuildFlags = strings.Join(parts, " ")
		}
	}
	for k, v := range e {
		if launchJSONKeysDropped[k] {
			continue
		}
		if cfg.Launch == nil {
			cfg.Launch = map[string]any{}
		}
		cfg.Launch[k] = x.expandAny(v)
	}
	if cfg.Program == "" && adapter == "go" {
		cfg.Program = root
	}
	if cfg.Program != "" && !filepath.IsAbs(cfg.Program) {
		cfg.Program = filepath.Join(root, cfg.Program)
	}
	if cfg.Cwd != "" && !filepath.IsAbs(cfg.Cwd) {
		cfg.Cwd = filepath.Join(root, cfg.Cwd)
	}
	if x.err != nil {
		return Config{}, false, x.err
	}
	return cfg, true, nil
}

// varExpander substitutes VS Code's ${...} variables. The first one it cannot
// resolve is kept in err: a path with a literal "${input:target}" in it would
// fail later with a far less helpful message.
type varExpander struct {
	root, file string
	err        error
}

var vscodeVar = regexp.MustCompile(`\$\{([^}]+)\}`)

func (x *varExpander) expand(s string) string {
	return vscodeVar.ReplaceAllStringFunc(s, func(ref string) string {
		name := ref[2 : len(ref)-1]
		if env, ok := strings.CutPrefix(name, "env:"); ok {
			return os.Getenv(env)
		}
		switch name {
		case "workspaceFolder", "workspaceRoot":
			return x.root
		case "workspaceFolderBasename":
			return filepath.Base(x.root)
		case "pathSeparator", "/":
			return string(filepath.Separator)
		case "file", "fileDirname", "fileBasename", "fileBasenameNoExtension", "relativeFile", "relativeFileDirname":
			if x.file == "" {
				x.fail(fmt.Errorf("uses ${%s}, which needs an open file", name))
				return ref
			}
			return fileVar(name, x.root, x.file)
		}
		x.fail(fmt.Errorf("uses ${%s}, which indigo does not support", name))
		return ref
	})
}

func (x *varExpander) fail(err error) {
	if x.err == nil {
		x.err = err
	}
}

func (x *varExpander) expandAny(v any) any {
	switch v := v.(type) {
	case string:
		return x.expand(v)
	case []any:
		out := make([]any, len(v))
		for i, e := range v {
			out[i] = x.expandAny(e)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, e := range v {
			out[k] = x.expandAny(e)
		}
		return out
	}
	return v
}

func fileVar(name, root, file string) string {
	rel := func(p string) string {
		if r, err := filepath.Rel(root, p); err == nil {
			return r
		}
		return p
	}
	switch name {
	case "file":
		return file
	case "fileDirname":
		return filepath.Dir(file)
	case "fileBasename":
		return filepath.Base(file)
	case "fileBasenameNoExtension":
		return strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))
	case "relativeFile":
		return rel(file)
	case "relativeFileDirname":
		return rel(filepath.Dir(file))
	}
	return ""
}

// stripJSONC turns VS Code's JSON-with-comments into JSON: // and /* */
// comments go (outside strings), and so do trailing commas before ] or }.
func stripJSONC(src []byte) []byte {
	out := make([]byte, 0, len(src))
	inStr, esc := false, false
	for i := 0; i < len(src); i++ {
		c := src[i]
		if inStr {
			out = append(out, c)
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch {
		case c == '"':
			inStr = true
			out = append(out, c)
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			for i < len(src) && src[i] != '\n' {
				i++
			}
			if i < len(src) {
				out = append(out, '\n')
			}
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			i += 2
			for i+1 < len(src) && (src[i] != '*' || src[i+1] != '/') {
				i++
			}
			i++ // past the closing '/'
		case c == ']' || c == '}':
			// Drop a comma left dangling before this bracket.
			j := len(out) - 1
			for j >= 0 && (out[j] == ' ' || out[j] == '\t' || out[j] == '\n' || out[j] == '\r') {
				j--
			}
			if j >= 0 && out[j] == ',' {
				out = append(out[:j], out[j+1:]...)
			}
			out = append(out, c)
		default:
			out = append(out, c)
		}
	}
	return out
}
