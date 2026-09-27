package dap

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// An adapter's stdout is drained for the whole session, including after a
// line too long for the scanner: otherwise the pipe fills and the adapter
// blocks on its next write. The script reports an address, writes a 200 KB
// line and as much again, then leaves a marker it can only reach if every
// write went through.
func TestStartListeningKeepsDrainingAfterALongLine(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() }) //nolint:errcheck
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { c.Close() }) //nolint:errcheck
		}
	}()
	marker := filepath.Join(t.TempDir(), "done")
	script := `echo "listening at ` + ln.Addr().String() + `"
head -c 200000 /dev/zero | tr '\0' x; echo
head -c 200000 /dev/zero | tr '\0' y; echo
touch "` + marker + `"
sleep 30`
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := StartListening(ctx, "sh", []string{"-c", script}, "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close() //nolint:errcheck
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the adapter blocked writing to stdout: output stopped being read after the long line")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A response always says whether it succeeded — the field is required, and a
// declined reverse request is exactly the case where it is false.
func TestResponseAlwaysCarriesSuccess(t *testing.T) {
	b, err := json.Marshal(message{Seq: 1, Type: "response", RequestSeq: 3, Command: "runInTerminal", Message: "no"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"success":false`) {
		t.Errorf("response %s has no success field", b)
	}
}
