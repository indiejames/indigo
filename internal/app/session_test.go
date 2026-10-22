package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/indiejames/indigo/internal/client"
	"github.com/indiejames/indigo/internal/config"
	"github.com/indiejames/indigo/internal/server"
)

// sessionHome points HOME at a temp dir, so the session file is never the
// developer's own, and returns where workDir's session is saved.
func sessionHome(t *testing.T, workDir string) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	p, err := sessionPath(workDir)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func writeSaved(t *testing.T, p string, s savedSession) {
	t.Helper()
	if err := writeSessionFile(p, s); err != nil {
		t.Fatal(err)
	}
}

func readSaved(t *testing.T, p string) savedSession {
	t.Helper()
	FlushSessionWrites(5 * time.Second)
	s, ok, _ := readSessionFile(p)
	if !ok {
		t.Fatalf("no session saved at %s", p)
	}
	return s
}

// groupFiles describes a saved session as "name:a.go,b.go other:c.go".
func groupFiles(s savedSession) string {
	var parts []string
	for _, g := range s.Groups {
		label := g.Name
		if label == "" {
			label = ungroupedLabel
		}
		parts = append(parts, label+":"+strings.Join(g.Files, ","))
	}
	return strings.Join(parts, " ")
}

func TestSessionFileRoundTripAndBadFiles(t *testing.T) {
	p := sessionHome(t, "/w")
	s := savedSession{Version: sessionVersion, Active: "api", Groups: []savedGroup{
		{Name: "api", Files: []string{"server.go", "sub/x.go"}, Active: "sub/x.go"},
		{Name: "", Files: []string{"README.md"}},
	}}
	writeSaved(t, p, s)
	got, ok, _ := readSessionFile(p)
	if !ok || !reflect.DeepEqual(got, s) {
		t.Fatalf("read back %+v, %v; want %+v", got, ok, s)
	}
	if info, err := os.Stat(p); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("session file mode = %v, %v; want 0600", info.Mode().Perm(), err)
	}

	os.WriteFile(p, []byte("{not json"), 0o600) //nolint:errcheck
	if _, ok, _ := readSessionFile(p); ok {
		t.Error("a malformed file was read as a session")
	}

	// A newer indigo's file is neither restored nor overwritten.
	newer := []byte(`{"version": 99, "groups": [{"name": "x", "files": ["a"]}]}`)
	os.WriteFile(p, newer, 0o600) //nolint:errcheck
	if _, ok, isNewer := readSessionFile(p); ok || !isNewer {
		t.Errorf("version 99: ok=%v newer=%v, want false true", ok, isNewer)
	}
	saveSession(p, s, false)
	if data, _ := os.ReadFile(p); string(data) != string(newer) {
		t.Errorf("a newer session was overwritten with %s", data)
	}
}

// TestMergeSessionAddsWithoutRemoving: a window started on a file only moves
// the files it has open; everything else in the saved session stays.
func TestMergeSessionAddsWithoutRemoving(t *testing.T) {
	base := savedSession{Version: sessionVersion, Active: "", Groups: []savedGroup{
		{Name: "api", Files: []string{"a.go", "b.go", "d.go"}, Active: "b.go"},
		{Name: "", Files: []string{"c.go"}},
	}}
	contrib := savedSession{Version: sessionVersion, Active: "web", Groups: []savedGroup{
		{Name: "api", Files: []string{"a.go", "d.go"}, Active: "d.go"},
		{Name: "web", Files: []string{"b.go", "e.go"}, Active: "e.go"},
	}}
	got := mergeSession(base, contrib)
	if want := "api:a.go,d.go ungrouped:c.go web:b.go,e.go"; groupFiles(got) != want {
		t.Errorf("merged = %q, want %q", groupFiles(got), want)
	}
	if got.Active != "" {
		t.Errorf("active = %q, want the saved session's own", got.Active)
	}
	if got.Groups[0].Active != "d.go" {
		t.Errorf("api's active = %q: b.go moved away, so the contribution's should be taken", got.Groups[0].Active)
	}
	// Nothing saved yet: the contribution is the session.
	if got := mergeSession(savedSession{}, contrib); groupFiles(got) != groupFiles(contrib) || got.Active != "web" {
		t.Errorf("merge into nothing = %q active %q", groupFiles(got), got.Active)
	}
}

// TestSessionIsWrittenOnlyOnceReadAndWhenItChanges: nothing is written before
// the saved session has been read, since the window's opening state would
// replace it, and closing the last buffer doesn't save an empty session.
func TestSessionIsWrittenOnlyOnceReadAndWhenItChanges(t *testing.T) {
	p := sessionHome(t, "/tmp")
	a := newGroupApp("a.go", "b.go")
	a.workDir, a.restoreSession = "/tmp", true
	a, _ = step(t, a, client.NextBufferMsg{})
	FlushSessionWrites(5 * time.Second)
	if _, err := os.Stat(p); err == nil {
		t.Fatal("the session was written before it had been read")
	}

	a, _ = step(t, a, sessionLoadedMsg{}) // nothing saved before
	if s := readSaved(t, p); groupFiles(s) != "ungrouped:a.go,b.go" || s.Groups[0].Active != "b.go" {
		t.Fatalf("saved %q, active %q", groupFiles(s), s.Groups[0].Active)
	}
	a = newGroupNamed(t, a, "api")
	if s := readSaved(t, p); groupFiles(s) != "ungrouped:a.go api:b.go" || s.Active != "api" {
		t.Fatalf("after a new group: %q, active group %q", groupFiles(s), s.Active)
	}
	a, _ = step(t, a, client.CloseBufferMsg{})
	a, _ = step(t, a, client.CloseBufferMsg{})
	if len(a.buffers) != 0 {
		t.Fatalf("%d buffers left", len(a.buffers))
	}
	if s := readSaved(t, p); groupFiles(s) != "ungrouped:a.go" {
		t.Errorf("after closing the last buffer: %q, want the session from just before", groupFiles(s))
	}
}

// TestFileStartUsesSavedGroupsAndAddsToThem: a window started on a file puts
// it, and every file it opens later, in its saved group, and adds its own
// changes to the saved session without dropping files it never opened.
func TestFileStartUsesSavedGroupsAndAddsToThem(t *testing.T) {
	p := sessionHome(t, "/tmp")
	writeSaved(t, p, savedSession{Version: sessionVersion, Groups: []savedGroup{
		{Name: "api", Files: []string{"a.go", "b.go"}},
		{Name: "", Files: []string{"c.go"}},
	}})
	a := newGroupApp("a.go")
	a.workDir = "/tmp"
	a, _ = step(t, a, a.loadSession()())
	if got := groupsOf(a); got != "api:1" || !a.showTabBar() {
		t.Fatalf("groups %q, tab bar %v: a.go should be in api", got, a.showTabBar())
	}
	if len(a.buffers) != 1 {
		t.Fatal("a window started on a file reopened the saved session")
	}

	a, _ = step(t, a, bufferOpenedMsg{model: newReloadTestModel(2, "/tmp/c.go"), line: -1, col: -1})
	a, _ = step(t, a, bufferOpenedMsg{model: newReloadTestModel(3, "/tmp/d.go"), line: -1, col: -1})
	// c.go goes to its saved group, which opening it makes current, so d.go
	// joins that one.
	if got := groupsOf(a); got != "api:1 ungrouped:2,3" {
		t.Fatalf("groups = %q", got)
	}
	if s := readSaved(t, p); groupFiles(s) != "api:a.go,b.go ungrouped:c.go,d.go" {
		t.Errorf("saved %q, want b.go kept although it was never opened, and a.go still first in api", groupFiles(s))
	}
}

// TestAReopenedFileGoesBackToItsGroup: within a window, a closed file opened
// again returns to its group, and a renamed group keeps its files.
func TestAReopenedFileGoesBackToItsGroup(t *testing.T) {
	a := newGroupApp("a.go", "b.go")
	a = newGroupNamed(t, a, "api")
	a, _ = step(t, a, client.TabGroupMsg{Action: client.TabGroupRename})
	a.pluginInput.text = ""
	a = confirm(t, typeText(t, a, "server"))
	a, _ = step(t, a, client.CloseBufferMsg{}) // a.go, the last of server
	a, _ = step(t, a, bufferOpenedMsg{model: newReloadTestModel(1, "/tmp/a.go"), line: -1, col: -1})
	if got := groupsOf(a); got != "ungrouped:2 server:1" {
		t.Errorf("groups = %q, want a.go back in server", got)
	}

	// A closed group is forgotten: its files rejoin the current group.
	a, _ = step(t, a, client.TabGroupMsg{Action: client.TabGroupClose})
	a, _ = step(t, a, bufferOpenedMsg{model: newReloadTestModel(1, "/tmp/a.go"), line: -1, col: -1})
	if got := groupsOf(a); got != "ungrouped:2,1" {
		t.Errorf("after closing server and reopening a.go: %q", got)
	}
}

// sessionWorkspace starts a real server on a temp workspace holding files.
func sessionWorkspace(t *testing.T, files ...string) (*client.RPC, string) {
	t.Helper()
	t.Setenv("INDIGO_LOG_DIR", t.TempDir())
	t.Setenv("INDIGO_PLUGINS_DIR", t.TempDir())
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	for _, f := range files {
		os.MkdirAll(filepath.Dir(filepath.Join(dir, f)), 0o755)                    //nolint:errcheck
		os.WriteFile(filepath.Join(dir, f), []byte("package x // "+f+"\n"), 0o644) //nolint:errcheck
	}
	srv, err := server.New(dir)
	if err != nil {
		t.Skipf("cannot start a server: %v", err)
	}
	t.Cleanup(srv.Wait)
	rpc, err := client.Dial(server.SocketPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		rpc.Disconnect(ctx) //nolint:errcheck
	})
	return rpc, dir
}

func bufferNames(a App) []string {
	out := make([]string, len(a.buffers))
	for i, m := range a.buffers {
		out[i] = filepath.Base(m.FilePath())
	}
	return out
}

// TestDirectoryStartRestoresTheSession drives a restore against a real
// server: the saved files reopen into their groups, a file deleted since is
// dropped and counted, the saved tabs are restored, and the start-up picker
// closes.
func TestDirectoryStartRestoresTheSession(t *testing.T) {
	rpc, dir := sessionWorkspace(t, "a.go", "b.go", "web/c.go")
	p := sessionHome(t, dir)
	writeSaved(t, p, savedSession{Version: sessionVersion, Active: "web", Groups: []savedGroup{
		{Name: "api", Files: []string{"a.go", "b.go"}, Active: "b.go"},
		{Name: "web", Files: []string{"web/c.go", "gone.go"}, Active: "web/c.go"},
	}})

	a := *NewWithPicker(rpc, &config.Config{}, dir)
	a, _ = step(t, a, tea.WindowSizeMsg{Width: 100, Height: 20})
	if a.picker == nil {
		t.Fatal("test setup: a directory start should open the picker")
	}
	a = run(t, a, a.loadSession(), 10*time.Second, 0)

	if got := bufferNames(a); !slices.Equal(got, []string{"a.go", "b.go", "c.go"}) {
		t.Fatalf("buffers = %v", got)
	}
	if got := groupsOf(a); got != "api:"+ids(a, "a.go", "b.go")+" web:"+ids(a, "c.go") {
		t.Errorf("groups = %q", got)
	}
	if filepath.Base(a.buffers[a.active].FilePath()) != "c.go" || a.groupName != "web" {
		t.Errorf("active %s in %q, want c.go in web", a.buffers[a.active].FilePath(), a.groupName)
	}
	if !strings.Contains(a.status, "1 file") {
		t.Errorf("status = %q, want the missing file counted", a.status)
	}
	if a.picker != nil {
		t.Error("the start-up picker is still open")
	}
	a, _ = step(t, a, client.TabGroupMsg{Action: client.TabGroupPrev})
	if filepath.Base(a.buffers[a.active].FilePath()) != "b.go" {
		t.Errorf("switching to api landed on %s, want its saved tab b.go", a.buffers[a.active].FilePath())
	}
	if s := readSaved(t, p); groupFiles(s) != "api:a.go,b.go web:web/c.go" {
		t.Errorf("saved after restore: %q, want gone.go dropped", groupFiles(s))
	}
}

// ids returns the BufIDs of the named buffers, as groupsOf prints them.
func ids(a App, names ...string) string {
	var out []string
	for _, n := range names {
		for _, m := range a.buffers {
			if filepath.Base(m.FilePath()) == n {
				out = append(out, jsonNum(m.BufID()))
			}
		}
	}
	return strings.Join(out, ",")
}

func jsonNum(n uint32) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// TestNoArgumentStartDropsItsEmptyBufferOnRestore: the untitled buffer an
// argument-less start opens gives way to the restored files, unless it has
// been typed in.
func TestNoArgumentStartDropsItsEmptyBufferOnRestore(t *testing.T) {
	rpc, dir := sessionWorkspace(t, "a.go")
	p := sessionHome(t, dir)
	writeSaved(t, p, savedSession{Version: sessionVersion, Groups: []savedGroup{{Name: "api", Files: []string{"a.go"}}}})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	bufID, content, version, _, gen, err := rpc.OpenFile(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	ap := New(rpc, bufID, content, version, "", &config.Config{}, false, dir, 0, gen)
	ap.RestoreSession()
	a := run(t, *ap, ap.loadSession(), 10*time.Second, 0)
	if got := bufferNames(a); !slices.Equal(got, []string{"a.go"}) {
		t.Errorf("buffers = %v, want the untitled one gone", got)
	}
	if groupsOf(a) != "api:"+ids(a, "a.go") {
		t.Errorf("groups = %q", groupsOf(a))
	}
}
