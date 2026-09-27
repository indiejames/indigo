package debug

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeLaunchJSON(t *testing.T, root, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, ".vscode"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, VSCodeLaunchFile), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestStripJSONC(t *testing.T) {
	src := `{
	// a line comment, with a "quote"
	"a": "http://not-a-comment", /* block
	comment */ "b": [1, 2,],
	"c": "escaped \" // still a string",
}`
	var got map[string]any
	if err := json.Unmarshal(stripJSONC([]byte(src)), &got); err != nil {
		t.Fatalf("%v\n%s", err, stripJSONC([]byte(src)))
	}
	want := map[string]any{"a": "http://not-a-comment", "b": []any{1.0, 2.0}, "c": `escaped " // still a string`}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// A launch.json as VS Code writes one: comments, trailing commas, variables,
// Go's "auto" mode, a debugpy entry, and entries indigo has no use for.
func TestLaunchJSONImport(t *testing.T) {
	root := t.TempDir()
	t.Setenv("INDIGO_TEST_TOKEN", "sekrit")
	writeLaunchJSON(t, root, `{
	"version": "0.2.0",
	"configurations": [
		// The package at the cursor's file.
		{
			"name": "Launch file's package",
			"type": "go",
			"request": "launch",
			"mode": "auto",
			"program": "${fileDirname}",
			"args": ["-config", "${workspaceFolder}/dev.toml"],
			"env": {"TOKEN": "${env:INDIGO_TEST_TOKEN}", "A": "1"},
			"buildFlags": ["-tags", "dev"],
			"dlvFlags": ["--check-go-version=false"],
			"console": "integratedTerminal",
		},
		{
			"name": "Test this file",
			"type": "go",
			"request": "launch",
			"mode": "auto",
			"program": "${file}"
		},
		{
			"name": "Python: current file",
			"type": "debugpy",
			"request": "launch",
			"program": "${file}",
			"justMyCode": false,
			"console": "integratedTerminal"
		},
		{
			"name": "Node: server",
			"type": "node",
			"request": "launch",
			"program": "${workspaceFolder}/src/server.ts",
			"runtimeExecutable": "tsx",
			"skipFiles": ["<node_internals>/**", "${workspaceFolder}/node_modules/**"],
			"console": "integratedTerminal"
		},
		{"name": "Attach", "type": "go", "request": "attach", "mode": "remote"},
		{"name": "Chrome", "type": "chrome", "request": "launch", "url": "http://localhost"},
	],
}`)
	active := filepath.Join(root, "pkg", "x_test.go")
	got, err := NewManager(nil).Launches(root, nil, active)
	if err != nil {
		t.Fatal(err)
	}
	want := []Config{
		{
			Name: "Launch file's package", Adapter: "go", Mode: "debug",
			Program:    filepath.Join(root, "pkg"),
			Args:       []string{"-config", root + "/dev.toml"},
			Env:        []string{"A=1", "TOKEN=sekrit"},
			BuildFlags: "-tags dev",
			Launch:     map[string]any{"dlvFlags": []any{"--check-go-version=false"}},
		},
		{Name: "Test this file", Adapter: "go", Mode: "test", Program: filepath.Join(root, "pkg")},
		{Name: "Python: current file", Adapter: "python", Program: active, Launch: map[string]any{"justMyCode": false}},
		{
			Name: "Node: server", Adapter: "node", Program: filepath.Join(root, "src", "server.ts"),
			Launch: map[string]any{
				"runtimeExecutable": "tsx",
				"skipFiles":         []any{"<node_internals>/**", root + "/node_modules/**"},
			},
		},
	}
	if !reflect.DeepEqual(got, want) {
		gj, _ := json.MarshalIndent(got, "", " ")
		wj, _ := json.MarshalIndent(want, "", " ")
		t.Errorf("got  %s\nwant %s", gj, wj)
	}
}

// An entry that cannot be used as written is reported by name and skipped;
// the others survive. A debug.toml entry wins a name clash with launch.json.
func TestLaunchJSONProblems(t *testing.T) {
	root := t.TempDir()
	writeLaunchJSON(t, root, `{"configurations": [
		{"name": "needs file", "type": "go", "request": "launch", "program": "${file}"},
		{"name": "input", "type": "go", "request": "launch", "program": "${input:pick}"},
		{"name": "odd mode", "type": "go", "request": "launch", "mode": "exec"},
		{"name": "shared", "type": "go", "request": "launch", "program": "./from-vscode"},
		{"name": "fine", "type": "go", "request": "launch"}
	]}`)
	writeProjectLaunches(t, root, "[[debug]]\nname = \"shared\"\nprogram = \"./from-toml\"\n")
	got, err := NewManager(nil).Launches(root, nil, "")
	for _, want := range []string{`"needs file": uses ${file}, which needs an open file`, `"input": uses ${input:pick}`, `"odd mode": mode "exec"`} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to mention %s", err, want)
		}
	}
	var names []string
	for _, c := range got {
		names = append(names, c.Name)
	}
	if !reflect.DeepEqual(names, []string{"shared", "fine"}) || got[0].Program != filepath.Join(root, "from-toml") {
		t.Errorf("got %+v, want debug.toml's shared then fine", got)
	}
	if got[1].Program != root {
		t.Errorf("a Go entry with no program should debug the workspace: %+v", got[1])
	}

	writeLaunchJSON(t, root, `{"configurations": [`)
	if _, err := NewManager(nil).Launches(root, nil, ""); err == nil || !strings.Contains(err.Error(), VSCodeLaunchFile) {
		t.Errorf("broken launch.json: err = %v", err)
	}
}
