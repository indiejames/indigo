package plugin

import (
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestDialSocketWaitsForTheListener is the regression test for an intermittent
// TestStartRefusesIncompatibleSDKVersion failure under a loaded full run, logged
// as "connect: connection refused". The manager waited for the plugin's socket
// file to exist and then dialled once. But the file appears when the plugin
// binds, and net.Listen only listens after that, so a plugin descheduled
// between the two refused the one dial and was never started.
//
// This holds a socket in exactly that state, bound but not listening, while the
// dial starts.
func TestDialSocketWaitsForTheListener(t *testing.T) {
	// Short, because a unix socket path is limited to about 100 bytes.
	dir, err := os.MkdirTemp("", "ds")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) }) //nolint:errcheck
	path := filepath.Join(dir, "p.sock")

	fd, err := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Bind(fd, &syscall.SockaddrUnix{Name: path}); err != nil {
		syscall.Close(fd) //nolint:errcheck
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("a bound socket should already have its file: %v", err)
	}
	if c, err := net.Dial("unix", path); err == nil {
		c.Close() //nolint:errcheck
		t.Fatal("dialling a bound socket that is not listening succeeded; the test reproduces nothing")
	}

	listening := make(chan error, 1)
	go func() {
		time.Sleep(200 * time.Millisecond)
		listening <- syscall.Listen(fd, 1)
	}()
	t.Cleanup(func() { syscall.Close(fd) }) //nolint:errcheck

	conn, err := dialSocket(path, 5*time.Second)
	if err != nil {
		t.Fatalf("dialSocket: %v; it must retry until the plugin is listening", err)
	}
	conn.Close() //nolint:errcheck
	if err := <-listening; err != nil {
		t.Fatal(err)
	}
}

// TestDialSocketGivesUp keeps the deadline: a plugin that never listens is
// refused after the timeout rather than waited for forever.
func TestDialSocketGivesUp(t *testing.T) {
	start := time.Now()
	if _, err := dialSocket(filepath.Join(t.TempDir(), "never.sock"), 100*time.Millisecond); err == nil {
		t.Fatal("dialSocket succeeded on a socket that never existed")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("dialSocket took %v to give up, want about its 100ms timeout", elapsed)
	}
}
