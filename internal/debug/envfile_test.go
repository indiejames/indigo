package debug

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/indiejames/indigo/internal/config"
)

func TestParseEnvFile(t *testing.T) {
	got, err := parseEnvFile([]byte(`# a comment

PLAIN=value
export EXPORTED=yes
SPACED = around
DOUBLE="two words\nand a newline \"quoted\""
SINGLE='literal \n kept # not a comment'
TRAILING=value # a trailing comment
EMPTY=
URL=http://host/#fragment
`))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"PLAIN=value", "EXPORTED=yes", "SPACED=around",
		"DOUBLE=two words\nand a newline \"quoted\"",
		`SINGLE=literal \n kept # not a comment`,
		"TRAILING=value", "EMPTY=", "URL=http://host/#fragment",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %q\nwant %q", got, want)
	}
	for _, bad := range []string{"NOEQUALS\n", "=value\n", "TWO WORDS=x\n"} {
		if _, err := parseEnvFile([]byte("OK=1\n" + bad)); err == nil || !strings.Contains(err.Error(), "line 2") {
			t.Errorf("%q: err = %v, want the line named", bad, err)
		}
	}
}

// The file's variables join the environment; the configuration's own env
// wins a clash; a missing file stops the launch with its name.
func TestWithEnvFile(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".env"), []byte("A=from-file\nB=from-file\n"), 0o644) //nolint:errcheck
	got, err := withEnvFile(Config{Cwd: dir, EnvFile: ".env", Env: []string{"B=from-config", "C=only-config"}})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"A=from-file", "B=from-config", "C=only-config"}; !reflect.DeepEqual(got.Env, want) {
		t.Errorf("env = %q, want %q", got.Env, want)
	}
	if _, err := withEnvFile(Config{Cwd: dir, EnvFile: "missing.env"}); err == nil || !strings.Contains(err.Error(), "missing.env") {
		t.Errorf("missing file: err = %v", err)
	}
	if got, _ := withEnvFile(Config{Env: []string{"X=1"}}); !reflect.DeepEqual(got.Env, []string{"X=1"}) {
		t.Errorf("no env file changed env: %q", got.Env)
	}
}

// The file is read when the session starts, so an edit shows up on the next
// restart — and what the adapter is started with carries it.
func TestEnvFileIsReadAtEachStart(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	os.WriteFile(envPath, []byte("V=first\n"), 0o644) //nolint:errcheck
	m := NewManager(nil)
	rec := &recordingAdapter{}
	m.startAdapter = rec.start
	cfg := Config{Adapter: "go", Program: dir, Cwd: dir, EnvFile: envPath}
	m.Start(cfg)                                       //nolint:errcheck // the recording adapter fails every launch
	os.WriteFile(envPath, []byte("V=second\n"), 0o644) //nolint:errcheck
	m.Restart(Config{})                                //nolint:errcheck
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.started) != 2 || !reflect.DeepEqual(rec.started[0].Env, []string{"V=first"}) || !reflect.DeepEqual(rec.started[1].Env, []string{"V=second"}) {
		t.Errorf("started with %+v", rec.started)
	}
	if m.last.Env != nil {
		t.Errorf("the file's variables were saved into the configuration kept for restart: %q", m.last.Env)
	}
}

// env_file in a named configuration is taken against the workspace, like its
// other paths; envFile in launch.json is read by indigo too, no longer
// dropped, and not passed on to the adapter.
func TestEnvFileInConfigurations(t *testing.T) {
	cfg := resolveLaunch("/w", config.DebugLaunch{Name: "x", EnvFile: ".env"})
	if cfg.EnvFile != "/w/.env" {
		t.Errorf("named: env file %q", cfg.EnvFile)
	}
	root := t.TempDir()
	writeLaunchJSON(t, root, `{"configurations": [
		{"name": "go", "type": "go", "request": "launch", "envFile": "${workspaceFolder}/.env.local"},
		{"name": "py", "type": "debugpy", "request": "launch", "program": "app.py", "envFile": "config/dev.env"}
	]}`)
	got, err := NewManager(nil).Launches(root, nil, "")
	if err != nil || len(got) != 2 {
		t.Fatalf("%+v, %v", got, err)
	}
	if got[0].EnvFile != root+"/.env.local" || got[1].EnvFile != filepath.Join(root, "config/dev.env") {
		t.Errorf("env files %q, %q", got[0].EnvFile, got[1].EnvFile)
	}
	if _, passed := got[1].Launch["envFile"]; passed {
		t.Error("envFile was passed through to the adapter as well")
	}
}

// debuggeeEnvCheck runs a program under a real debugger with env_file set and
// checks what it printed: a variable only the file sets, and one both set,
// where the configuration's env wins.
func debuggeeEnvCheck(t *testing.T, m *Manager, cfg Config) {
	t.Helper()
	os.WriteFile(filepath.Join(cfg.Cwd, ".env"), []byte("# test\nFROM_DOTENV=\"from the file\"\nBOTH=file\n"), 0o644) //nolint:errcheck
	cfg.EnvFile = ".env"
	cfg.Env = append(cfg.Env, "BOTH=config")
	if err := m.Start(cfg); err != nil {
		t.Fatalf("Start: %v", err)
	}
	deadline := time.Now().Add(60 * time.Second)
	for {
		chunks, _, _ := m.Output(0)
		var out strings.Builder
		for _, c := range chunks {
			out.WriteString(c.Text)
		}
		s := out.String()
		if strings.Contains(s, "FROM_DOTENV=from the file") && strings.Contains(s, "BOTH=config") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the program did not see the .env variables; output:\n%s", s)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestEnvFileWithDelve(t *testing.T) {
	canDebug(t)
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nimport (\n\t\"fmt\"\n\t\"os\"\n)\n\nfunc main() {\n\tfmt.Printf(\"FROM_DOTENV=%s BOTH=%s\\n\", os.Getenv(\"FROM_DOTENV\"), os.Getenv(\"BOTH\"))\n}\n"), 0o644) //nolint:errcheck
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module d\n\ngo 1.21\n"), 0o644)                                                                                                                                                 //nolint:errcheck
	m := NewManager(nil)
	t.Cleanup(m.Shutdown)
	debuggeeEnvCheck(t, m, Config{Adapter: "go", Program: dir, Cwd: dir})
}

func TestEnvFileWithDebugpy(t *testing.T) {
	python := os.Getenv("INDIGO_DEBUGPY_PYTHON")
	if python == "" {
		python = "python3"
	}
	if testing.Short() || exec.Command(python, "-c", "import debugpy").Run() != nil {
		t.Skip("debugpy not installed (set INDIGO_DEBUGPY_PYTHON)")
	}
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	script := filepath.Join(dir, "app.py")
	os.WriteFile(script, []byte("import os\nprint('FROM_DOTENV=' + os.environ.get('FROM_DOTENV', '') + ' BOTH=' + os.environ.get('BOTH', ''))\n"), 0o644) //nolint:errcheck
	m := NewManager(nil)
	t.Cleanup(m.Shutdown)
	adapters := append([]config.DebugAdapter(nil), config.DefaultDebugAdapters...)
	adapters[0].Command = python
	m.SetAdapters(adapters)
	debuggeeEnvCheck(t, m, Config{Program: script, Cwd: dir})
}

func TestEnvFileWithJSDebug(t *testing.T) {
	adapters := jsDebugAdapter(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	file := filepath.Join(dir, "app.ts")
	os.WriteFile(file, []byte("const a: string = process.env.FROM_DOTENV ?? '';\nconsole.log(`FROM_DOTENV=${a} BOTH=${process.env.BOTH}`);\n"), 0o644) //nolint:errcheck
	m := NewManager(nil)
	t.Cleanup(m.Shutdown)
	m.SetAdapters(adapters)
	debuggeeEnvCheck(t, m, Config{Program: file, Cwd: dir})
}

func TestEnvFileWithLLDB(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and debugs a program")
	}
	if _, err := findCommand("lldb-dap"); err != nil {
		t.Skip("lldb-dap not installed")
	}
	cc, err := exec.LookPath("cc")
	if err != nil {
		t.Skip("no C compiler")
	}
	if os.Getenv("INDIGO_DEBUG_TESTS") != "1" {
		if hint := PermissionHint(); hint != "" {
			t.Skip(hint)
		}
	}
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	src, bin := filepath.Join(dir, "p.c"), filepath.Join(dir, "p")
	os.WriteFile(src, []byte("#include <stdio.h>\n#include <stdlib.h>\nint main(void) {\n\tprintf(\"FROM_DOTENV=%s BOTH=%s\\n\", getenv(\"FROM_DOTENV\"), getenv(\"BOTH\"));\n\treturn 0;\n}\n"), 0o644) //nolint:errcheck
	if out, err := exec.Command(cc, "-g", "-o", bin, src).CombinedOutput(); err != nil {
		t.Skipf("cannot compile: %v %s", err, out)
	}
	m := NewManager(nil)
	t.Cleanup(m.Shutdown)
	debuggeeEnvCheck(t, m, Config{Adapter: "lldb", Program: bin, Cwd: dir})
}

// A .env that sets NODE_OPTIONS keeps it when indigo adds its tsx preload:
// the file is merged first, so the preload is added to it rather than
// replacing it.
func TestEnvFileNodeOptionsWithTSXPreload(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	preload, _ := TSXPreloadPath()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".env"), []byte("NODE_OPTIONS=--max-old-space-size=4096\n"), 0o644) //nolint:errcheck
	cfg, err := withEnvFile(Config{Cwd: dir, EnvFile: ".env", Launch: map[string]any{"runtimeExecutable": "tsx"}})
	if err != nil {
		t.Fatal(err)
	}
	cfg = withTSXPreload(cfg)
	if want := []string{"NODE_OPTIONS=--max-old-space-size=4096 --require " + preload}; !reflect.DeepEqual(cfg.Env, want) {
		t.Errorf("env = %q, want %q", cfg.Env, want)
	}
}
