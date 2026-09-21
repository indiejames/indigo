package client

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/indiejames/indigo/internal/server"
)

// dialWorkspaceServer brings up a real server over an in-memory stream and
// returns a connected client, so these exercise the same path a container
// would: no shared filesystem assumption anywhere in the call.
func dialWorkspaceServer(t *testing.T, workDir string) *RPC {
	t.Helper()
	t.Setenv("INDIGO_LOG_DIR", t.TempDir())

	srv, err := server.New(workDir)
	if err != nil {
		t.Skipf("cannot start a server here: %v", err)
	}
	t.Cleanup(srv.Wait)

	r, err := Dial(server.SocketPath(workDir))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		r.Disconnect(ctx) //nolint:errcheck
	})
	return r
}

func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestListDirOverRPC(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"b.txt":              "b",
		"a.txt":              "a",
		"sub/c.txt":          "c",
		"node_modules/x.txt": "x",
	})
	r := dialWorkspaceServer(t, dir)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// An empty path means the workspace root — the caller does not have to know
	// what that is on the server's side.
	entries, err := r.ListDir(ctx, "")
	if err != nil {
		t.Fatalf("ListDir: %v", err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name)
	}
	// Directories first, then files, each alphabetical — the order the picker
	// browses in. node_modules is absent: browse mode has always hidden ignored
	// directories, and the set that says which lives on the server now.
	want := []string{"sub", "a.txt", "b.txt"}
	if len(names) != len(want) {
		t.Fatalf("entries = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Errorf("entries = %v, want %v", names, want)
			break
		}
	}
	for _, e := range entries {
		if e.Name == "sub" && !e.IsDir {
			t.Error("sub was not reported as a directory")
		}
		if e.Name == "a.txt" && e.IsDir {
			t.Error("a.txt was reported as a directory")
		}
	}
}

func TestListWorkspaceFilesOverRPCSkipsIgnoredDirs(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"a.txt":               "a",
		"sub/b.txt":           "b",
		"node_modules/dep.js": "dep",
		".git/config":         "cfg",
	})
	r := dialWorkspaceServer(t, dir)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	paths, err := r.ListWorkspaceFiles(ctx)
	if err != nil {
		t.Fatalf("ListWorkspaceFiles: %v", err)
	}
	got := map[string]bool{}
	for _, p := range paths {
		got[p] = true
	}
	if !got["a.txt"] || !got[filepath.Join("sub", "b.txt")] {
		t.Errorf("paths = %v, want the real files", paths)
	}
	// The ignore set is applied on the server, from the server's config — which
	// in a container is the container's, not the client's.
	if got[filepath.Join("node_modules", "dep.js")] || got[filepath.Join(".git", "config")] {
		t.Errorf("paths = %v, want ignored directories skipped", paths)
	}
}

func TestGrepWorkspaceOverRPC(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"a.txt":     "nothing here\nfindme once\n",
		"sub/b.txt": "findme twice\nfindme again\n",
		"c.txt":     "unrelated\n",
	})
	r := dialWorkspaceServer(t, dir)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	results, searchErr, err := r.GrepWorkspace(ctx, "findme", "", "", false, false, false)
	if err != nil {
		t.Fatalf("GrepWorkspace: %v", err)
	}
	if searchErr != "" {
		t.Fatalf("search error: %s", searchErr)
	}
	if len(results) != 3 {
		t.Fatalf("got %d results, want 3: %+v", len(results), results)
	}
	for _, res := range results {
		if res.MatchLen != len("findme") {
			t.Errorf("match length = %d, want %d: %+v", res.MatchLen, len("findme"), res)
		}
		if res.LineText == "" {
			t.Errorf("result carries no line text: %+v", res)
		}
	}
}

// TestGrepWorkspaceReportsAPatternErrorWithoutFailingTheCall pins the
// distinction the schema comment draws: search-as-you-type sends half-typed
// regexes constantly, so a bad pattern is a normal answer and not a broken
// connection. Returning it as an RPC failure would route it into the path that
// means the server is in trouble.
func TestGrepWorkspaceReportsAPatternErrorWithoutFailingTheCall(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"a.txt": "hello\n"})
	r := dialWorkspaceServer(t, dir)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	results, searchErr, err := r.GrepWorkspace(ctx, `\[unclosed`, "", "", false, false, false)
	if err != nil {
		t.Fatalf("a bad pattern failed the call itself: %v", err)
	}
	if searchErr == "" {
		t.Fatal("a bad regex produced no error message")
	}
	if len(results) != 0 {
		t.Errorf("got %d results alongside an error", len(results))
	}
}

func TestFilterWorkspaceFilesOverRPC(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"kept.txt":            "k",
		"node_modules/dep.js": "d",
	})
	r := dialWorkspaceServer(t, dir)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	kept, err := r.FilterWorkspaceFiles(ctx, []string{
		"kept.txt",
		"gone.txt",                              // recorded, since deleted
		filepath.Join("node_modules", "dep.js"), // under an ignored directory
	})
	if err != nil {
		t.Fatalf("FilterWorkspaceFiles: %v", err)
	}
	if len(kept) != 1 || kept[0] != "kept.txt" {
		t.Errorf("kept = %v, want only kept.txt", kept)
	}
}

// TestStatPathOverRPC covers the three answers the New File prompt reacts to
// differently. "Missing" must not arrive as an error: the prompt offers to
// create a missing directory and refuses to guess past anything else, so
// collapsing the two would make it offer to create things it cannot.
func TestStatPathOverRPC(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"sub/f.txt": "f"})
	r := dialWorkspaceServer(t, dir)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	exists, isDir, err := r.StatPath(ctx, filepath.Join(dir, "sub"))
	if err != nil || !exists || !isDir {
		t.Errorf("stat of a directory = (%v, %v, %v), want (true, true, nil)", exists, isDir, err)
	}

	exists, isDir, err = r.StatPath(ctx, filepath.Join(dir, "sub", "f.txt"))
	if err != nil || !exists || isDir {
		t.Errorf("stat of a file = (%v, %v, %v), want (true, false, nil)", exists, isDir, err)
	}

	exists, _, err = r.StatPath(ctx, filepath.Join(dir, "nope"))
	if err != nil {
		t.Errorf("stat of a missing path returned an error: %v — absent is an answer", err)
	}
	if exists {
		t.Error("a missing path reported as existing")
	}
}

func TestCreateDirOverRPC(t *testing.T) {
	dir := t.TempDir()
	r := dialWorkspaceServer(t, dir)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// Missing intermediate components are created too, which is what the New
	// File prompt needs when someone types a whole path at once.
	target := filepath.Join(dir, "a", "b", "c")
	if err := r.CreateDir(ctx, target); err != nil {
		t.Fatalf("CreateDir: %v", err)
	}
	info, err := os.Stat(target)
	if err != nil || !info.IsDir() {
		t.Fatalf("directory was not created: %v", err)
	}
}
