// Command indigo-server is the editor server on its own, for running inside a
// dev container.
//
// It exists as a separate entry point for one reason: it must cross-compile.
// cmd/indigo cannot — internal/highlight pulls in a cgo tree-sitter binding
// whatever the build tags say, so CGO_ENABLED=0 fails to build it at all. The
// server never needed any of that: syntax highlighting is entirely client-side,
// and internal/server has no dependency on internal/highlight. So this builds
// static, for any Linux the container happens to be, and the host copies it in
// rather than asking anyone to modify their image.
//
// It speaks capnp over stdin/stdout and exits when that stream closes. Nothing
// may write to stdout here — stdout is the wire.
package main

import (
	"fmt"
	"os"

	"github.com/indiejames/indigo/internal/hangdetect"
	"github.com/indiejames/indigo/internal/server"
)

// stdioStream presents stdin and stdout as one bidirectional stream. Closing it
// closes stdin only: stdout carries the last bytes of a capnp teardown, and
// closing both here would race that.
type stdioStream struct{}

func (stdioStream) Read(p []byte) (int, error)  { return os.Stdin.Read(p) }
func (stdioStream) Write(p []byte) (int, error) { return os.Stdout.Write(p) }
func (stdioStream) Close() error                { return os.Stdin.Close() }

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintf(os.Stderr, "usage: %s <workspace-dir>\n", os.Args[0])
		os.Exit(2)
	}
	hangdetect.Start()
	defer hangdetect.Stop()

	srv, err := server.ServeStream(os.Args[1], stdioStream{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "indigo-server: %v\n", err)
		os.Exit(1)
	}
	srv.Wait()
}
