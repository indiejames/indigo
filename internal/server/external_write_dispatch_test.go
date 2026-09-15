package server

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/indiejames/indigo/internal/config"
	"github.com/indiejames/indigo/internal/document"
	proto "github.com/indiejames/indigo/internal/proto"
)

// fileChangedCallback is a partial proto.ClientCallback_Server implementing
// only FileChanged. onFileChanged may block, standing in for a wedged client.
type fileChangedCallback struct {
	proto.ClientCallback_Server
	onFileChanged func()
}

func (f *fileChangedCallback) FileChanged(_ context.Context, call proto.ClientCallback_fileChanged) error {
	f.onFileChanged()
	_, err := call.AllocResults()
	return err
}

// TestHandleExternalWriteDoesNotBlockOnOneWedgedClient is a regression test.
// handleExternalWrite used to fan out to clients serially, waiting on each
// fut.Struct() with context.Background() — no timeout. Since it runs on
// watchLoop's single goroutine, one wedged or slow client stalled
// external-change detection for *every* file the server watches, not just its
// own: no further fsnotify event was processed until that client answered.
//
// The assertion is on handleExternalWrite's own return, not on notification
// order, which makes it independent of map iteration order: the pre-fix code
// waited for every client regardless of the order it visited them in, so it
// could never return before the wedged one unblocked.
func TestHandleExternalWriteDoesNotBlockOnOneWedgedClient(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "watched.go")

	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	notified := make(chan struct{}, 1)

	wedged := proto.ClientCallback_ServerToClient(&fileChangedCallback{
		onFileChanged: func() { <-release },
	})
	defer wedged.Release()
	responsive := proto.ClientCallback_ServerToClient(&fileChangedCallback{
		onFileChanged: func() { notified <- struct{}{} },
	})
	defer responsive.Release()

	s := &editorService{
		cfg: &config.Config{},
		buffers: map[uint32]*bufferEntry{
			1: {
				buf:       document.New(path, "package main\n"),
				canonPath: canonicalPath(path),
				clients:   map[uint64]struct{}{1: {}, 2: {}},
			},
		},
		clientMap: map[uint64]*clientEntry{
			1: {callback: wedged},
			2: {callback: responsive},
		},
		savingPaths: make(map[string]time.Time),
		dirWatches:  make(map[string]int),
	}

	done := make(chan time.Duration, 1)
	go func() {
		start := time.Now()
		s.handleExternalWrite(path)
		done <- time.Since(start)
	}()

	select {
	case elapsed := <-done:
		if elapsed > time.Second {
			t.Errorf("handleExternalWrite took %v; a wedged client must not hold up the watch loop", elapsed)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("handleExternalWrite never returned — one wedged client is stalling the whole watch loop")
	}

	// The responsive client must still actually be notified: returning fast by
	// dropping everybody would pass the check above but break the feature.
	select {
	case <-notified:
	case <-time.After(2 * time.Second):
		t.Error("the responsive client was never notified of the external write")
	}
}
