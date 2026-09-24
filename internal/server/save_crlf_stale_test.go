package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/indiejames/indigo/internal/config"
	"github.com/indiejames/indigo/internal/document"
	"github.com/indiejames/indigo/internal/format"
	"github.com/indiejames/indigo/internal/lint"
	"github.com/indiejames/indigo/internal/lsp"
	"github.com/indiejames/indigo/internal/plugin"
	proto "github.com/indiejames/indigo/internal/proto"
)

// TestSaveUsesTheSwappedBuffersLineEndings covers FormatOnSave's stale branch:
// when the buffer is replaced while the formatter runs, Save discards the
// formatted result and writes the buffer's current content — and must write it
// with the *current* buffer's line endings, not the ones it read before
// formatting. Here a reload swaps an LF buffer for a CRLF one mid-format.
func TestSaveUsesTheSwappedBuffersLineEndings(t *testing.T) {
	dir := t.TempDir()
	startedPath := filepath.Join(dir, "started")
	filePath := filepath.Join(dir, "x.slow")
	cfg := &config.Config{
		FormatOnSave: true,
		Formatters: []config.FormatterConfig{
			{Extensions: []string{"slow"}, Command: "sh", Args: []string{"-c", "touch " + startedPath + " && sleep 1 && tr a-z A-Z"}},
		},
	}
	s := &editorService{
		cfg:         cfg,
		buffers:     map[uint32]*bufferEntry{1: {buf: document.New(filePath, "hello\n")}},
		fmtMgr:      format.NewManager(nil, cfg, dir),
		lspMgr:      &lsp.Manager{},
		lintMgr:     &lint.Manager{},
		pluginMgr:   &plugin.Manager{},
		dirWatches:  make(map[string]int),
		savingPaths: make(map[string]time.Time),
	}
	client := proto.EditorService_ServerToClient(&connSvc{editorService: s, connID: 1})

	saveDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		fut, rel := client.Save(ctx, func(p proto.EditorService_save_Params) error {
			p.SetBufferId(1)
			return nil
		})
		defer rel()
		_, err := fut.Struct()
		saveDone <- err
	}()

	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(startedPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("formatter never started")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// What ReloadBuffer does when the file on disk turned out to be CRLF.
	s.mu.Lock()
	s.buffers[1].buf = document.New(filePath, "hi\n")
	s.buffers[1].crlf = true
	s.buffers[1].generation++
	s.mu.Unlock()

	if err := <-saveDone; err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if want := "hi\r\n"; string(got) != want {
		t.Errorf("saved %q, want %q — the swapped-in buffer's CRLF endings were not used", got, want)
	}
}
