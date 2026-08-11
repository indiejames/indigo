package plugin

import (
	"net"
	"os/exec"
	"testing"
	"time"

	"capnproto.org/go/capnp/v3/rpc"
)

// TestReapOnDisconnectReapsProcessWhenConnCloses is a regression test: no
// dispatch path ever called proc.Wait(), so a plugin process left a zombie
// entry (crash mid-session, or normal Shutdown's Kill-without-Wait) until
// the whole server process exited. reapOnDisconnect must reap the process
// as soon as its RPC connection closes.
func TestReapOnDisconnectReapsProcessWhenConnCloses(t *testing.T) {
	cmd := exec.Command("sleep", "100")
	if err := cmd.Start(); err != nil {
		t.Skipf("could not start test child process: %v", err)
	}

	clientEnd, serverEnd := net.Pipe()
	defer serverEnd.Close() //nolint:errcheck
	rpcConn := rpc.NewConn(rpc.NewStreamTransport(clientEnd), nil)

	done := reapOnDisconnect(cmd.Process, rpcConn)

	select {
	case <-done:
		t.Fatal("reaping completed before the connection was even closed")
	case <-time.After(50 * time.Millisecond):
	}

	// Simulate the crash: the process dies, which is what would actually
	// cause its RPC connection to close in production.
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if err := rpcConn.Close(); err != nil {
		t.Fatalf("rpcConn.Close: %v", err)
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("process was never reaped after rpcConn closed")
	}

	// A second Wait on an already-reaped process must fail — confirming
	// reapOnDisconnect's Wait call actually consumed the child's exit
	// status rather than being a no-op.
	if _, err := cmd.Process.Wait(); err == nil {
		t.Error("expected the second Wait to error (process already reaped), got nil")
	}
}
