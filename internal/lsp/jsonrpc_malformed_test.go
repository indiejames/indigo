package lsp

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"
)

// TestCallFailsFastOnMalformedResponse is a regression test: a response
// body that fails to unmarshal into jsonrpcMsg used to be silently dropped
// (readLoop's `continue`), leaving the matching pending Call() blocked until
// its own context deadline even though the server clearly replied. When the
// id is still independently recoverable (the common case: a broken
// result/params payload inside an otherwise-intact envelope), the call
// should fail immediately instead.
func TestCallFailsFastOnMalformedResponse(t *testing.T) {
	clientEnd, serverEnd := net.Pipe()
	defer clientEnd.Close() //nolint:errcheck
	defer serverEnd.Close() //nolint:errcheck

	conn := newJSONRPCConn(clientEnd, clientEnd, nil, nil)

	callDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := conn.Call(ctx, "initialize", struct{}{})
		callDone <- err
	}()

	// Read the request off the pipe so we know the id the client used (it's
	// always 1, the first Call on a fresh conn), then write back a reply
	// whose envelope is intact but whose "result" is not valid JSON.
	buf := make([]byte, 4096)
	if _, err := serverEnd.Read(buf); err != nil {
		t.Fatalf("failed to read request: %v", err)
	}

	// error.code is typed int in jsonrpcMsg; sending it as a string is a
	// syntactically valid document that still fails Unmarshal into the
	// struct (a type mismatch, not a syntax error) — the realistic shape
	// this fix targets, since a genuine syntax error breaks tokenization of
	// the whole document and makes even the id unrecoverable.
	body := []byte(`{"jsonrpc":"2.0","id":1,"error":{"code":"boom","message":"parse trouble"}}`)
	frame := fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(body), body)
	start := time.Now()
	if _, err := serverEnd.Write([]byte(frame)); err != nil {
		t.Fatalf("failed to write malformed response: %v", err)
	}

	var err error
	select {
	case err = <-callDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Call() never returned after a malformed response with a recoverable id — it should fail fast, not wait out its own timeout")
	}
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Call() succeeded on a malformed response, want an error")
	}
	if elapsed > time.Second {
		t.Fatalf("Call() took %v to fail after the malformed response — want near-instant", elapsed)
	}
}

// TestCallStillTimesOutOnUnrecoverableMalformedBody verifies the fallback
// path when even the id can't be recovered (body isn't valid JSON at all):
// the message is dropped as before, and the call is left to its own
// deadline rather than the process panicking or blocking readLoop.
func TestCallStillTimesOutOnUnrecoverableMalformedBody(t *testing.T) {
	clientEnd, serverEnd := net.Pipe()
	defer clientEnd.Close() //nolint:errcheck
	defer serverEnd.Close() //nolint:errcheck

	conn := newJSONRPCConn(clientEnd, clientEnd, nil, nil)

	callDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		_, err := conn.Call(ctx, "initialize", struct{}{})
		callDone <- err
	}()

	buf := make([]byte, 4096)
	if _, err := serverEnd.Read(buf); err != nil {
		t.Fatalf("failed to read request: %v", err)
	}

	body := []byte(`not json at all`)
	frame := fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(body), body)
	if _, err := serverEnd.Write([]byte(frame)); err != nil {
		t.Fatalf("failed to write malformed response: %v", err)
	}

	select {
	case err := <-callDone:
		if err == nil {
			t.Fatal("Call() succeeded on an unparseable response, want an error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Call() never returned even after its own context deadline — readLoop must have wedged")
	}
}
