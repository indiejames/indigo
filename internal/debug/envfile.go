package debug

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// .env files. A configuration's EnvFile is read by indigo itself when the
// session starts — not handed to the adapter — so it works the same for every
// debugger: Delve, debugpy and lldb-dap have no such setting of their own, and
// js-debug's is its alone. Read at start rather than when configurations are
// listed, so an edit to the file shows up on the next start or restart.

// parseEnvFile reads the dotenv format as the common tools write it:
//
//	# comment
//	KEY=value
//	export KEY=value          (the shell-compatible spelling)
//	KEY="double quoted\nwith escapes"
//	KEY='single quoted, taken literally'
//	KEY=value # trailing comment (unquoted values only)
//
// Blank lines are skipped. A line that is not KEY=value is an error naming its
// line number: a typo that silently drops a variable is the harder bug to find.
func parseEnvFile(data []byte) ([]string, error) {
	var out []string
	sc := bufio.NewScanner(bytes.NewReader(data))
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		key, value, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" || strings.ContainsAny(key, " \t") {
			return nil, fmt.Errorf("line %d is not KEY=value", n)
		}
		value = strings.TrimSpace(value)
		switch {
		case len(value) >= 2 && value[0] == '"' && strings.HasSuffix(value, `"`):
			value = strings.NewReplacer(`\n`, "\n", `\t`, "\t", `\"`, `"`, `\\`, `\`).Replace(value[1 : len(value)-1])
		case len(value) >= 2 && value[0] == '\'' && strings.HasSuffix(value, "'"):
			value = value[1 : len(value)-1]
		default:
			if i := strings.Index(value, " #"); i >= 0 {
				value = strings.TrimSpace(value[:i])
			}
		}
		out = append(out, key+"="+value)
	}
	return out, sc.Err()
}

// withEnvFile merges cfg's EnvFile into its Env: the file's variables first,
// then Env's own entries, which win a clash — as VS Code orders "envFile" and
// "env". A relative path is taken against the working directory.
func withEnvFile(cfg Config) (Config, error) {
	if cfg.EnvFile == "" {
		return cfg, nil
	}
	path := cfg.EnvFile
	if !filepath.IsAbs(path) && cfg.Cwd != "" {
		path = filepath.Join(cfg.Cwd, path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("env_file: %w", err)
	}
	fromFile, err := parseEnvFile(data)
	if err != nil {
		return cfg, fmt.Errorf("env_file %s: %w", path, err)
	}
	set := map[string]bool{}
	for _, kv := range cfg.Env {
		k, _, _ := strings.Cut(kv, "=")
		set[k] = true
	}
	env := make([]string, 0, len(fromFile)+len(cfg.Env))
	for _, kv := range fromFile {
		if k, _, _ := strings.Cut(kv, "="); !set[k] {
			env = append(env, kv)
		}
	}
	cfg.Env = append(env, cfg.Env...)
	return cfg, nil
}
