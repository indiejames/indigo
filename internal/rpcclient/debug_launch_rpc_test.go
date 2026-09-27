package rpcclient

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/indiejames/indigo/internal/server"
)

// Named configurations and restart, through a real server: the workspace's
// .indigo/debug.toml is read and resolved against the workspace, and a restart
// reruns what was last started. The unsupported adapter makes each launch fail
// at once, so no debugger is needed to see which configuration was chosen.
func TestDebugConfigsAndRestartOverRPC(t *testing.T) {
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	t.Setenv("INDIGO_LOG_DIR", t.TempDir())
	t.Setenv("INDIGO_PLUGINS_DIR", t.TempDir())
	os.MkdirAll(filepath.Join(dir, ".indigo"), 0o755) //nolint:errcheck
	err := os.WriteFile(filepath.Join(dir, ".indigo", "debug.toml"), []byte(`
[[debug]]
name = "rpc-test-config"
adapter = "cobol"
program = "./cmd/x"
args = ["-v"]
env = { A = "1" }
`), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := server.New(dir)
	if err != nil {
		t.Skipf("cannot start a server here: %v", err)
	}
	t.Cleanup(srv.Wait)
	r, err := Dial(server.SocketPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		r.Disconnect(ctx) //nolint:errcheck
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cfgs, err := r.DebugConfigs(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	// The user's own config.toml may add entries after the workspace's.
	if len(cfgs) == 0 {
		t.Fatal("no configurations")
	}
	want := DebugConfig{
		Name: "rpc-test-config", Adapter: "cobol", Mode: "debug",
		Program: filepath.Join(dir, "cmd/x"), Args: []string{"-v"}, Env: []string{"A=1"},
	}
	if !reflect.DeepEqual(cfgs[0], want) {
		t.Errorf("got  %+v\nwant %+v", cfgs[0], want)
	}

	started, err := r.DebugRestart(ctx, DebugConfig{Adapter: "cobol", Program: filepath.Join(dir, "fallback")})
	if err == nil || !strings.Contains(err.Error(), "cobol") {
		t.Errorf("err = %v, want the unsupported adapter reported", err)
	}
	if started.Program != filepath.Join(dir, "fallback") || started.Cwd != dir {
		t.Errorf("first restart started %+v, want the fallback run in the workspace", started)
	}
	r.DebugStart(ctx, cfgs[0]) //nolint:errcheck // fails by design
	started, _ = r.DebugRestart(ctx, DebugConfig{Adapter: "cobol", Program: filepath.Join(dir, "fallback")})
	if started.Name != "rpc-test-config" || !reflect.DeepEqual(started.Env, []string{"A=1"}) {
		t.Errorf("restart started %+v, want the configuration started last", started)
	}
}
