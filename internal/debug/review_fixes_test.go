package debug

import (
	"errors"
	"net"
	"os/exec"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/indiejames/indigo/internal/dap"
)

// "export" is a prefix before a space or a tab, and part of the name
// otherwise.
func TestEnvFileExportPrefix(t *testing.T) {
	got, err := parseEnvFile([]byte("export\tTABBED=1\nexport  SPACED=2\nexportFOO=3\n"))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"TABBED=1", "SPACED=2", "exportFOO=3"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

// A numeric processId in another adapter's attach entry becomes the process id
// indigo attaches with — for Node that is what switches its inspector on —
// and is not also passed through raw.
func TestLaunchJSONNumericProcessIDForOtherAdapters(t *testing.T) {
	root := t.TempDir()
	writeLaunchJSON(t, root, `{"configurations": [
		{"name": "node pid", "type": "node", "request": "attach", "processId": 4321},
		{"name": "py pid", "type": "debugpy", "request": "attach", "processId": 99, "justMyCode": false}
	]}`)
	got, err := NewManager(nil).Launches(root, nil, "")
	if err != nil || len(got) != 2 {
		t.Fatalf("%+v, %v", got, err)
	}
	if got[0].ProcessID != 4321 || got[0].Launch != nil {
		t.Errorf("node: %+v, want process id 4321 and nothing passed raw", got[0])
	}
	if got[1].ProcessID != 99 || !reflect.DeepEqual(got[1].Launch, map[string]any{"justMyCode": false}) {
		t.Errorf("py: %+v", got[1])
	}
}

// The inspector's script must be matched from a path separator: x.js is not
// .../index.js.
func TestScriptMatches(t *testing.T) {
	for _, tc := range []struct {
		url  string
		args []string
		want bool
	}{
		{"file:///w/src/index.ts", []string{"src/index.ts"}, true},
		{"file:///w/src/index.ts", []string{"/w/src/index.ts"}, true},
		{"file:///w/src/index.ts", []string{"--inspect", "index.ts"}, true},
		{"file:///w/index.js", []string{"x.js"}, false},
		{"file:///w/myindex.ts", []string{"index.ts"}, false},
		{"file:///w/index.ts", []string{"-e"}, false},
	} {
		if got := scriptMatches(tc.url, tc.args); got != tc.want {
			t.Errorf("%s %q: got %v", tc.url, tc.args, got)
		}
	}
}

// SIGUSR1 terminates most programs, so attaching to Node by process id
// signals only a Node process: a Go program's pid is refused and the program
// survives. And a second start while a session is active is refused before
// anything is signalled.
func TestNodeAttachNeverSignalsOtherPrograms(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	saved := nodeInspectorAddr
	nodeInspectorAddr = ln.Addr().String() // nothing listens: the signalling path
	ln.Close()                             //nolint:errcheck
	t.Cleanup(func() { nodeInspectorAddr = saved })

	victim := startProc(t, buildGoBinary(t)) // dies on SIGUSR1, like most programs
	time.Sleep(200 * time.Millisecond)
	alive := func() bool {
		return victim.Process.Signal(syscall.Signal(0)) == nil && !processStopped(victim.Process.Pid)
	}

	if _, err := attachNodeByPID(Config{ProcessID: victim.Process.Pid}); err == nil || !strings.Contains(err.Error(), "not a Node program") {
		t.Errorf("err = %v, want the non-Node process refused", err)
	}
	time.Sleep(200 * time.Millisecond)
	if !alive() {
		t.Fatal("the Go program was signalled and killed")
	}

	m := NewManager(nil)
	clientEnd, other := net.Pipe()
	t.Cleanup(func() { other.Close() }) //nolint:errcheck
	m.client = dap.NewClient(clientEnd, nil)
	_, err := m.StartWithWarning(Config{Adapter: "node", Request: "attach", ProcessID: victim.Process.Pid})
	if !errors.Is(err, ErrSessionActive) {
		t.Errorf("second start: err = %v, want ErrSessionActive", err)
	}
	time.Sleep(200 * time.Millisecond)
	if !alive() {
		t.Error("a refused second start signalled the process")
	}
}
