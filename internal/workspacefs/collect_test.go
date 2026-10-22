package workspacefs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"capnproto.org/go/capnp/v3"
)

func collectTree(t *testing.T, n int) string {
	t.Helper()
	dir := t.TempDir()
	for i := range n {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%03d.txt", i)), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestCollectFilesStopsAtItsSizeBudget: the list goes to the window in one RPC
// message, so a workspace with too many files is listed in part rather than
// sent whole. Unbounded, a home directory's listing outgrew the client's read
// limit and cost the window its connection.
func TestCollectFilesStopsAtItsSizeBudget(t *testing.T) {
	dir := collectTree(t, 50)
	defer func(old int) { MaxCollectBytes = old }(MaxCollectBytes)
	MaxCollectBytes = 10 * (len("f000.txt") + collectEntryOverhead)

	paths, truncated := CollectFiles(context.Background(), dir, IgnoredDirs())
	if !truncated || len(paths) != 10 {
		t.Errorf("got %d paths, truncated=%v; want 10, truncated", len(paths), truncated)
	}

	MaxCollectBytes = 1 << 20
	paths, truncated = CollectFiles(context.Background(), dir, IgnoredDirs())
	if truncated || len(paths) != 50 {
		t.Errorf("under budget: got %d paths, truncated=%v; want all 50", len(paths), truncated)
	}
}

// TestCollectFilesStopsWhenCancelled: the server passes the call's context,
// which ends when the picker stops waiting, so an abandoned scan stops too.
func TestCollectFilesStopsWhenCancelled(t *testing.T) {
	dir := collectTree(t, 20)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	paths, truncated := CollectFiles(ctx, dir, IgnoredDirs())
	if !truncated || len(paths) != 0 {
		t.Errorf("got %d paths, truncated=%v; want none, truncated", len(paths), truncated)
	}
}

// TestCollectBudgetFitsInOneRPCMessage: the budget must leave room under the
// limit the receiving end decodes with, or the bound protects nothing.
func TestCollectBudgetFitsInOneRPCMessage(t *testing.T) {
	var d capnp.Decoder
	limit := d.MaxMessageSize
	if limit == 0 {
		limit = 64 << 20 // capnp's defaultDecodeLimit, used when the field is 0
	}
	if uint64(MaxCollectBytes) > limit/2 {
		t.Errorf("MaxCollectBytes = %d, want at most half the %d-byte decode limit", MaxCollectBytes, limit)
	}
}

// TestAHomeDirectoryIsListedOnlyNearTheTop: a workspace that is the home
// directory is a whole user's files, so the walk stops BroadRootDepth
// directories down and says the list is partial. The same tree as a project
// is listed in full.
func TestAHomeDirectoryIsListedOnlyNearTheTop(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{"top.md", "a/one.md", "a/b/c/three.md", "a/b/c/d/four.md"} {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	all := []string{"a/b/c/d/four.md", "a/b/c/three.md", "a/one.md", "top.md"}

	defer func(old func() (string, error)) { homeDir = old }(homeDir)
	homeDir = func() (string, error) { return t.TempDir(), nil }
	paths, truncated := CollectFiles(context.Background(), root, IgnoredDirs())
	if truncated || !slices.Equal(paths, all) {
		t.Errorf("as a project: %v truncated=%v, want all of %v", paths, truncated, all)
	}

	homeDir = func() (string, error) { return root, nil }
	paths, truncated = CollectFiles(context.Background(), root, IgnoredDirs())
	if want := all[1:]; !truncated || !slices.Equal(paths, want) {
		t.Errorf("as home: %v truncated=%v, want %v, truncated", paths, truncated, want)
	}
}

// cancelAfter is a context that reads as cancelled from its n+1th Err call.
type cancelAfter struct {
	context.Context
	n int
}

func (c *cancelAfter) Err() error {
	if c.n <= 0 {
		return context.Canceled
	}
	c.n--
	return nil
}

// TestCollectFilesStopsPartWayThroughADirectory: cancellation is checked as
// entries are read, so a directory bigger than one read batch is abandoned
// part way through rather than read to the end first.
func TestCollectFilesStopsPartWayThroughADirectory(t *testing.T) {
	dir := collectTree(t, collectBatch*2+10)
	ctx := &cancelAfter{Context: context.Background(), n: collectBatch + 5}
	paths, truncated := CollectFiles(ctx, dir, IgnoredDirs())
	if !truncated || len(paths) != collectBatch+5 {
		t.Errorf("got %d paths, truncated=%v; want %d, truncated", len(paths), truncated, collectBatch+5)
	}
}

// TestCollectFilesKeepsTheShallowestFilesWhenCutShort: the walk is
// breadth-first, so a list the budget cuts short holds the files nearest the
// root, which are the likeliest to be wanted. Each branch has a near file and a
// deeper one, so a depth-first walk reaches a deep file before the second
// near file whichever branch it takes first (ReadDir order is the
// filesystem's, not sorted).
func TestCollectFilesKeepsTheShallowestFilesWhenCutShort(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{"a/n.md", "a/y/d.md", "z/n.md", "z/y/d.md"} {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	defer func(old int) { MaxCollectBytes = old }(MaxCollectBytes)
	MaxCollectBytes = 2 * (len("a/n.md") + collectEntryOverhead)
	paths, truncated := CollectFiles(context.Background(), root, IgnoredDirs())
	if want := []string{"a/n.md", "z/n.md"}; !truncated || !slices.Equal(paths, want) {
		t.Errorf("got %v truncated=%v, want %v, truncated", paths, truncated, want)
	}
}

// TestCollectFilesListsASymlinkWithoutFollowingIt, as filepath.WalkDir did:
// following one could walk out of the workspace, or round a loop.
func TestCollectFilesListsASymlinkWithoutFollowingIt(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "real"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "real", "f.md"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(root, filepath.Join(root, "loop")); err != nil {
		t.Fatal(err)
	}
	paths, truncated := CollectFiles(context.Background(), root, IgnoredDirs())
	if want := []string{"loop", "real/f.md"}; truncated || !slices.Equal(paths, want) {
		t.Errorf("got %v truncated=%v, want %v", paths, truncated, want)
	}
}
