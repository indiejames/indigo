package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/indiejames/indigo/internal/debuglog"
	proto "github.com/indiejames/indigo/internal/proto"
)

// An accepted ApplyOps is written to the shared log, with both the number of
// ops sent and the number actually applied.
//
// Regression test for a diagnostic gap rather than a behaviour bug: only
// rejections were logged, so three reports of "the tool said it edited the
// file and the buffer never changed" arrived with nothing on the server side
// to examine. applied < sent is the shape that distinguishes an edit that
// rebased away to nothing — which applyRebased treats as success — from one
// that never arrived at all, and neither was recorded.
func TestSuccessfulApplyOpsIsLogged(t *testing.T) {
	logDir := t.TempDir()
	t.Setenv("INDIGO_LOG_DIR", logDir)

	_, cl := newApplyOpsTestService(t, "hello\n", 0)
	ctx := context.Background()

	fut, rel := cl.ApplyOps(ctx, func(p proto.EditorService_applyOps_Params) error {
		p.SetClientId(1)
		p.SetBufferId(1)
		p.SetGeneration(0)
		p.SetBaseVersion(0)
		list, err := p.NewOps(1)
		if err != nil {
			return err
		}
		op := list.At(0)
		op.SetType(proto.EditOp_OpType_insert)
		op.SetInsertLine(0)
		op.SetInsertCol(0)
		return op.SetInsertText("x")
	})
	defer rel()
	if _, err := fut.Struct(); err != nil {
		t.Fatalf("ApplyOps: %v", err)
	}

	entries, err := debuglog.Read(debuglog.ReadOptions{Since: time.Now().Add(-time.Minute), Max: 500})
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	var line string
	for _, e := range entries {
		if strings.Contains(e.Raw, "ApplyOps: buffer 1") {
			line = e.Raw
		}
	}
	if line == "" {
		t.Fatalf("no ApplyOps line in the log; got %d entries", len(entries))
	}
	for _, want := range []string{"client 1", "sent 1 op(s)", "applied 1", "version 1"} {
		if !strings.Contains(line, want) {
			t.Errorf("log line %q is missing %q", line, want)
		}
	}
}
