package client

import (
	"context"
	"net"
	"os"
	"testing"
	"time"

	"github.com/indiejames/indigo/internal/binstamp"
	"github.com/indiejames/indigo/internal/server"
)

// TestAWindowExpectingAnotherBuildIsToldTheServerIsStale: in a dev container
// a window joins the workspace's running server, whose binary is never
// replaced on disk (each build has its own path), so only the build the
// window expected can show that the server is an older one. A window that
// names the server's own build, or none, is not told.
func TestAWindowExpectingAnotherBuildIsToldTheServerIsStale(t *testing.T) {
	t.Setenv("INDIGO_LOG_DIR", t.TempDir())
	dir := t.TempDir()
	srv, err := server.New(dir)
	if err != nil {
		t.Skipf("cannot start a server here: %v", err)
	}
	exe, err := os.Executable() // the server runs in this process
	if err != nil {
		t.Fatal(err)
	}
	own, err := binstamp.ContentHash(exe)
	if err != nil {
		t.Fatal(err)
	}

	waited := false
	for _, tc := range []struct {
		name, expected string
		stale          bool
	}{
		{"the server's own build", own, false},
		{"another build", "0123456789abcdef", true},
		{"no expectation", "", false},
	} {
		conn, err := net.Dial("unix", server.SocketPath(dir))
		if err != nil {
			t.Fatalf("%s: dial: %v", tc.name, err)
		}
		r, err := DialStreamExpecting(conn, tc.expected)
		if err != nil {
			t.Fatalf("%s: connect: %v", tc.name, err)
		}
		if !waited {
			t.Cleanup(srv.Wait) // see dialWorkspaceServer: only once there is a client
			waited = true
		}
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			r.Disconnect(ctx) //nolint:errcheck
		})
		if r.ServerStale() != tc.stale {
			t.Errorf("%s: ServerStale = %v, want %v", tc.name, r.ServerStale(), tc.stale)
		}
	}
}
