package container

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestHasDevcontainerConfig(t *testing.T) {
	cases := map[string]string{
		"folder":     ".devcontainer/devcontainer.json",
		"root file":  ".devcontainer.json",
		"named conf": ".devcontainer/python/devcontainer.json",
	}
	for name, rel := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, rel)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte("{}"), 0o644); err != nil {
				t.Fatal(err)
			}
			if !HasDevcontainerConfig(dir) {
				t.Errorf("HasDevcontainerConfig missed %s", rel)
			}
		})
	}
	if HasDevcontainerConfig(t.TempDir()) {
		t.Error("HasDevcontainerConfig reported a config in an empty directory")
	}
}

func TestMountedAt(t *testing.T) {
	mounts := "/var/run/docker.sock\t/var/run/docker.sock\n" +
		"/Users/me\t/home/me\n" +
		"/Users/me/src/api\t/workspaces/api\n"
	cases := []struct {
		host, want string
		ok         bool
	}{
		// The workspace mount beats the home-directory mount above it.
		{"/Users/me/src/api", "/workspaces/api", true},
		{"/Users/me/src/api/cmd", "/workspaces/api/cmd", true},
		{"/Users/me/notes", "/home/me/notes", true},
		// A shared prefix is not containment.
		{"/Users/me/src/api2", "/home/me/src/api2", true},
		{"/opt/elsewhere", "", false},
	}
	for _, c := range cases {
		got, ok := mountedAt(mounts, c.host)
		if got != c.want || ok != c.ok {
			t.Errorf("mountedAt(%q) = %q, %v; want %q, %v", c.host, got, ok, c.want, c.ok)
		}
	}
}

type fakeProbe struct {
	id      string
	psErr   error
	label   string // the folder RunningDevcontainer must be asked about; "" for any
	mounts  string // docker inspect mount lines
	servers []string
}

func (f fakeProbe) RunningDevcontainer(_ context.Context, folder string) (string, error) {
	if f.label != "" && folder != f.label {
		return "", f.psErr
	}
	return f.id, f.psErr
}
func (f fakeProbe) MountedAt(_ context.Context, _, hostPath string) (string, bool, error) {
	dest, ok := mountedAt(f.mounts, hostPath)
	return dest, ok, nil
}
func (f fakeProbe) ServerWorkspaces(context.Context, string) ([]string, error) {
	return f.servers, nil
}

func TestFindDevcontainerSession(t *testing.T) {
	ctx := context.Background()
	const host = "/Users/me/src/api"
	const mounts = "/Users/me/src/api\t/workspaces/api\n"

	withServer := fakeProbe{id: "abc", mounts: mounts, servers: []string{"/workspaces/api"}}
	s, found, err := FindDevcontainerSession(ctx, withServer, host, host)
	if err != nil || !found || s.ContainerID != "abc" || s.WorkspaceDir != "/workspaces/api" || s.HostWorkspaceDir != host {
		t.Errorf("with a server inside: got %+v, %v, %v", s, found, err)
	}

	// A running container with no indigo server in it (VS Code, or every
	// indigo window already closed) has nothing to join.
	noServer := fakeProbe{id: "abc", mounts: mounts}
	if _, found, _ := FindDevcontainerSession(ctx, noServer, host, host); found {
		t.Error("reported a session in a container with no indigo server")
	}

	// A server for a different workspace in the same container does not count.
	otherWorkspace := fakeProbe{id: "abc", mounts: mounts, servers: []string{"/workspaces/web"}}
	if _, found, _ := FindDevcontainerSession(ctx, otherWorkspace, host, host); found {
		t.Error("reported a session for another workspace's server")
	}

	if _, found, _ := FindDevcontainerSession(ctx, fakeProbe{}, host, host); found {
		t.Error("reported a session with no container running")
	}

	down := fakeProbe{psErr: errors.New("Cannot connect to the Docker daemon")}
	if _, found, err := FindDevcontainerSession(ctx, down, host, host); found || err == nil {
		t.Errorf("docker down: found=%v err=%v, want not found with the error", found, err)
	}
}

// TestFindDevcontainerSessionFromASubdirectory is the limitation this was
// written for: `indigo --devcontainer .` at the repository root, then
// `indigo .` in a subdirectory. The window's folder is not the container's
// label nor the server's workspace, and the check used to ask about exactly
// that folder on both counts, so it found nothing and the window started a
// second server holding a copy of the same files.
func TestFindDevcontainerSessionFromASubdirectory(t *testing.T) {
	ctx := context.Background()
	const root = "/Users/me/src/api"
	p := fakeProbe{id: "abc", label: root, mounts: root + "\t/workspaces/api\n",
		servers: []string{"/workspaces/api"}}

	s, found, err := FindDevcontainerSession(ctx, p, root, root+"/cmd/server")
	if err != nil || !found {
		t.Fatalf("from a subdirectory: found=%v err=%v, want the root's session", found, err)
	}
	if s.WorkspaceDir != "/workspaces/api" || s.HostWorkspaceDir != root {
		t.Errorf("joined %q (host %q), want the root's server", s.WorkspaceDir, s.HostWorkspaceDir)
	}
}

func TestFindDevcontainerSessionPicksTheNearestServer(t *testing.T) {
	ctx := context.Background()
	const root = "/Users/me/src/api"
	p := fakeProbe{id: "abc", mounts: root + "\t/workspaces/api\n", servers: []string{
		"/workspaces/api",
		"/workspaces/api/web",
		"/workspaces/api/web/static", // below the window's folder: not all of its files
		"/workspaces/api2",           // a shared prefix is not a parent
		"/workspaces/api/we",
	}}
	s, found, err := FindDevcontainerSession(ctx, p, root, root+"/web/src")
	if err != nil || !found || s.WorkspaceDir != "/workspaces/api/web" || s.HostWorkspaceDir != root+"/web" {
		t.Errorf("got %+v, %v, %v; want the server for web", s, found, err)
	}

	// Only servers that are not at or above the window's folder.
	p.servers = []string{"/workspaces/api2", "/workspaces/api/web/static", "/workspaces/api/we"}
	if s, found, _ := FindDevcontainerSession(ctx, p, root, root+"/web"); found {
		t.Errorf("joined %+v, which does not hold this folder's files", s)
	}
}

// A server above the dev container's own folder — for a parent mount, say the
// user's home — is not this dev container's session.
func TestFindDevcontainerSessionStaysInsideTheDevcontainerFolder(t *testing.T) {
	ctx := context.Background()
	p := fakeProbe{id: "abc",
		mounts:  "/Users/me\t/home/me\n/Users/me/src/api\t/workspaces/api\n",
		servers: []string{"/home/me"}}
	if s, found, _ := FindDevcontainerSession(ctx, p, "/Users/me/src/api", "/Users/me/src/api"); found {
		t.Errorf("joined %+v, a server outside the dev container's folder", s)
	}
}

func TestFindDevcontainerRoot(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "repo")
	sub := filepath.Join(root, "cmd", "server")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, ok := FindDevcontainerRoot(sub); ok {
		// Nothing above a fresh temp dir should have one, but a machine
		// could; say what was found rather than fail obscurely.
		t.Skip("a directory above the temp dir has a devcontainer.json")
	}
	if err := os.MkdirAll(filepath.Join(root, ".devcontainer"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".devcontainer", "devcontainer.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{root, sub, sub + "/"} {
		if got, ok := FindDevcontainerRoot(dir); !ok || got != root {
			t.Errorf("FindDevcontainerRoot(%q) = %q, %v; want %q", dir, got, ok, root)
		}
	}
}

func TestParseServerWorkspaces(t *testing.T) {
	got := parseServerWorkspaces("/workspaces/api\n\n/workspaces/my api \n")
	want := []string{"/workspaces/api", "/workspaces/my api"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("got %q, want %q", got, want)
	}
}
