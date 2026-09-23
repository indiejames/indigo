package container

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func strp(s string) *string { return &s }

// The case the feature exists for: a project extends PATH with remoteEnv so the
// server — and the language servers it spawns — can find tools installed
// outside the image's ENV PATH. The reference must resolve against the
// container, not be passed through literally or resolved against this machine.
func TestResolveRemoteEnvExtendsTheContainersPath(t *testing.T) {
	got := ResolveRemoteEnv(
		map[string]*string{"PATH": strp("${containerEnv:PATH}:/home/vscode/.local/bin")},
		map[string]string{"PATH": "/usr/local/bin:/usr/bin"},
		func(string) (string, bool) { return "/host/path/must/not/leak", true },
	)
	want := []string{"PATH=/usr/local/bin:/usr/bin:/home/vscode/.local/bin"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolveRemoteEnvForms(t *testing.T) {
	local := map[string]string{"HOME_ON_HOST": "/Users/me"}
	got := ResolveRemoteEnv(map[string]*string{
		"B_DEFAULT":   strp("${containerEnv:MISSING:fallback}"),
		"A_LOCAL":     strp("${localEnv:HOME_ON_HOST}/cache"),
		"C_EMPTY":     strp("x${containerEnv:MISSING}y"),
		"D_OTHER_VAR": strp("${containerWorkspaceFolder}/bin"),
		"E_UNSET":     nil, // the spec's "unset": carried to indigo-server by name
		"F_PLAIN":     strp("literal"),
		"G_UNSET_TOO": nil,
		"H_PRESENT":   strp("[${containerEnv:EMPTY:fallback}]"),
	}, map[string]string{"EMPTY": ""}, func(k string) (string, bool) { v, ok := local[k]; return v, ok })
	want := []string{
		"A_LOCAL=/Users/me/cache",
		"B_DEFAULT=fallback",
		"C_EMPTY=xy",
		"D_OTHER_VAR=${containerWorkspaceFolder}/bin",
		"F_PLAIN=literal",
		// Present but empty is kept empty, as the devcontainer CLI does; the
		// default is only for an absent variable.
		"H_PRESENT=[]",
		UnsetEnvVar + "=E_UNSET,G_UNSET_TOO",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got\n %q\nwant\n %q", got, want)
	}
}

// read-configuration: remoteEnv from the file and from features' merged view.
// The file wins, and the merged view is accepted as an object or an array.
func TestReadConfigurationMergesRemoteEnv(t *testing.T) {
	for name, merged := range map[string]string{
		"merged object": `{"PATH":"${containerEnv:PATH}:/feature/bin","FEATURE_ONLY":"1"}`,
		"merged array":  `[{"PATH":"${containerEnv:PATH}:/feature/bin"},{"FEATURE_ONLY":"1"}]`,
	} {
		t.Run(name, func(t *testing.T) {
			out := `{"configuration":{"remoteEnv":{"PATH":"${containerEnv:PATH}:/project/bin"}},` +
				`"mergedConfiguration":{"remoteEnv":` + merged + `}}`
			cfg, err := fakeCLI(t, out, "", 0).ReadConfiguration(context.Background(), "/w")
			if err != nil {
				t.Fatal(err)
			}
			if p := cfg.RemoteEnv["PATH"]; p == nil || !strings.HasSuffix(*p, "/project/bin") {
				t.Errorf("PATH = %v, want the project's own value to win", p)
			}
			if f := cfg.RemoteEnv["FEATURE_ONLY"]; f == nil || *f != "1" {
				t.Errorf("FEATURE_ONLY = %v, want a feature's contribution kept", f)
			}
		})
	}
}

// A file whose only relevant setting is remoteEnv must still be read — the
// match that picks the result line used to consider only the indigo block,
// shutdownAction and compose.
func TestReadConfigurationReadsAFileWithOnlyRemoteEnv(t *testing.T) {
	cli := fakeCLI(t, `{"configuration":{"image":"x","remoteEnv":{"GOPATH":"/go"}}}`, "", 0)
	cfg, err := cli.ReadConfiguration(context.Background(), "/w")
	if err != nil {
		t.Fatal(err)
	}
	if g := cfg.RemoteEnv["GOPATH"]; g == nil || *g != "/go" {
		t.Errorf("GOPATH = %v, want /go", g)
	}
}

// Attach must hand remoteEnv to the server's exec — that environment is what
// the daemon and its language servers inherit — without letting it displace
// INDIGO_PLUGINS_DIR, which comes last so it wins.
func TestAttachPassesRemoteEnvToTheServer(t *testing.T) {
	rt := &fakeRuntime{arch: "arm64", exists: true}
	locate, _, _ := locateReal(t, "a server binary")
	stream, err := Attach(context.Background(), rt, "c1", "/workspaces/proj", AttachOptions{
		Locate:      locate,
		Env:         []string{"PATH=/usr/bin:/extra", "INDIGO_PLUGINS_DIR=/wrong"},
		PluginsDir:  t.TempDir(),
		PluginsHash: "abc",
	})
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	stream.Close() //nolint:errcheck

	if len(rt.execEnv) == 0 || rt.execEnv[0] != "PATH=/usr/bin:/extra" {
		t.Errorf("exec env = %q, want remoteEnv passed to the server", rt.execEnv)
	}
	if last := rt.execEnv[len(rt.execEnv)-1]; last != "INDIGO_PLUGINS_DIR="+PluginsPath("abc") {
		t.Errorf("last exec env = %q, want INDIGO_PLUGINS_DIR last so remoteEnv cannot override it", last)
	}
}

// When the container's environment cannot be read, a PATH extension must be
// dropped rather than resolved to ":/extra", which would replace PATH outright.
func TestWithoutContainerEnvRefsKeepsPathIntact(t *testing.T) {
	got := ResolveRemoteEnv(WithoutContainerEnvRefs(map[string]*string{
		"PATH":   strp("${containerEnv:PATH}:/extra"),
		"GOPATH": strp("/go"),
	}), nil, nil)
	if want := []string{"GOPATH=/go"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q (PATH must be left to the image)", got, want)
	}
}

// remoteEnv values can be secrets. They must reach docker through its
// environment (`-e NAME`), never its argv (`-e NAME=value`), which any local
// user can read with ps.
func TestExecEnvKeepsValuesOffTheCommandLine(t *testing.T) {
	d := Docker{Command: "docker", User: "vscode"}
	env := []string{"API_TOKEN=s3cret", "EMPTY=", "PATH=/usr/bin:/extra"}
	cmd := d.execEnvCommand(context.Background(), "c1", env, []string{"/srv", "/w"})

	args := strings.Join(cmd.Args, " ")
	if strings.Contains(args, "s3cret") || strings.Contains(args, "/extra") {
		t.Errorf("a value is on the command line: %q", cmd.Args)
	}
	for _, name := range []string{"-e API_TOKEN", "-e EMPTY", "-e PATH"} {
		if !strings.Contains(args, name) {
			t.Errorf("command line %q is missing %q", args, name)
		}
	}
	// The values are in docker's environment, after the inherited one so they
	// win a duplicated name (PATH especially).
	last := map[string]string{}
	for _, kv := range cmd.Env {
		k, v, _ := strings.Cut(kv, "=")
		last[k] = v
	}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		if got, ok := last[k]; !ok || got != v {
			t.Errorf("docker's environment has %s=%q (present=%v), want %q", k, got, ok, v)
		}
	}
}
